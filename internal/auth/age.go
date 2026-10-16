package auth

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"filippo.io/age"
	"filippo.io/age/armor"
)

const (
	keyFileName  = "fbm.key"
	armorHeader  = "-----BEGIN AGE ENCRYPTED FILE-----"
	binaryHeader = "age-encryption.org/v1"
)

// Keygen creates an age X25519 identity in dir/fbm.key (0600) and returns the
// matching recipient string. It never overwrites: an existing key file yields
// an error wrapping os.ErrExist.
func Keygen(dir string) (string, error) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return "", fmt.Errorf("generate age identity: %w", err)
	}
	path := filepath.Join(dir, keyFileName)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("create key file %s: %w", path, err)
	}
	if _, err := io.WriteString(f, id.String()+"\n"); err != nil {
		f.Close()
		os.Remove(path)
		return "", fmt.Errorf("write key file %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("close key file %s: %w", path, err)
	}
	return id.Recipient().String(), nil
}

// DecryptIfArmored returns the contents of path, decrypted with the identity in
// keyFile when it is an age file (armored or binary). Any other content is
// returned unchanged, so callers can accept plain and encrypted exports alike.
func DecryptIfArmored(path, keyFile string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	// Pasted chat blocks often gain leading blank lines.
	trimmed := bytes.TrimLeft(data, " \t\r\n")
	armored := bytes.HasPrefix(trimmed, []byte(armorHeader))
	if !armored && !bytes.HasPrefix(trimmed, []byte(binaryHeader)) {
		return data, nil
	}

	kf, err := os.Open(keyFile)
	if err != nil {
		return nil, fmt.Errorf("open key file %s: %w", keyFile, err)
	}
	defer kf.Close()
	ids, err := age.ParseIdentities(kf)
	if err != nil {
		return nil, fmt.Errorf("parse key file %s: %w", keyFile, err)
	}

	var src io.Reader = bytes.NewReader(trimmed)
	if armored {
		src = armor.NewReader(src)
	}
	r, err := age.Decrypt(src, ids...)
	if err != nil {
		return nil, fmt.Errorf("decrypt %s: %w", path, err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("decrypt %s: %w", path, err)
	}
	return out, nil
}
