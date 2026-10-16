package auth_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	"filippo.io/age/armor"
	"github.com/stretchr/testify/require"

	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/auth"
)

// encryptForTest age-encrypts plain to recipient and returns armored output.
func encryptForTest(t *testing.T, recipient string, plain []byte) []byte {
	t.Helper()
	r, err := age.ParseX25519Recipient(recipient)
	require.NoError(t, err)
	var buf bytes.Buffer
	aw := armor.NewWriter(&buf)
	w, err := age.Encrypt(aw, r)
	require.NoError(t, err)
	_, err = w.Write(plain)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	require.NoError(t, aw.Close())
	return buf.Bytes()
}

// encryptBinaryForTest is encryptForTest without armor.
func encryptBinaryForTest(t *testing.T, recipient string, plain []byte) []byte {
	t.Helper()
	r, err := age.ParseX25519Recipient(recipient)
	require.NoError(t, err)
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, r)
	require.NoError(t, err)
	_, err = w.Write(plain)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return buf.Bytes()
}

func TestKeygenThenDecryptRoundTrip(t *testing.T) {
	dir := t.TempDir()
	pub, err := auth.Keygen(dir)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(pub, "age1"))
	st, err := os.Stat(filepath.Join(dir, "fbm.key"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), st.Mode().Perm())

	enc := encryptForTest(t, pub, []byte(`[{"name":"xs","value":"v"}]`))
	p := filepath.Join(dir, "c.age")
	require.NoError(t, os.WriteFile(p, enc, 0o600))
	got, err := auth.DecryptIfArmored(p, filepath.Join(dir, "fbm.key"))
	require.NoError(t, err)
	require.Equal(t, `[{"name":"xs","value":"v"}]`, string(got))
}

func TestDecryptIfArmored_BinaryAndLeadingWhitespace(t *testing.T) {
	dir := t.TempDir()
	pub, err := auth.Keygen(dir)
	require.NoError(t, err)
	key := filepath.Join(dir, "fbm.key")
	plain := []byte("secret-payload")

	bin := filepath.Join(dir, "bin.age")
	require.NoError(t, os.WriteFile(bin, encryptBinaryForTest(t, pub, plain), 0o600))
	got, err := auth.DecryptIfArmored(bin, key)
	require.NoError(t, err)
	require.Equal(t, plain, got)

	// Chat transport can prepend blank lines or spaces to a pasted block.
	ws := filepath.Join(dir, "ws.age")
	padded := append([]byte("\r\n  \n"), encryptForTest(t, pub, plain)...)
	require.NoError(t, os.WriteFile(ws, padded, 0o600))
	got, err = auth.DecryptIfArmored(ws, key)
	require.NoError(t, err)
	require.Equal(t, plain, got)
}

func TestKeygenRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	_, err := auth.Keygen(dir)
	require.NoError(t, err)
	before, err := os.ReadFile(filepath.Join(dir, "fbm.key"))
	require.NoError(t, err)

	_, err = auth.Keygen(dir)
	require.Error(t, err)
	require.True(t, errors.Is(err, os.ErrExist), "got %v", err)

	after, err := os.ReadFile(filepath.Join(dir, "fbm.key"))
	require.NoError(t, err)
	require.Equal(t, before, after, "existing key must be untouched")
}

func TestDecryptIfArmored_PlainPassthrough(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "plain.json")
	plain := []byte(`[{"name":"xs","value":"v"}]`)
	require.NoError(t, os.WriteFile(p, plain, 0o600))
	// keyFile need not exist for plaintext input.
	got, err := auth.DecryptIfArmored(p, filepath.Join(dir, "missing.key"))
	require.NoError(t, err)
	require.Equal(t, plain, got)
}

func TestDecryptIfArmored_WrongKeyFailsWithoutLeaking(t *testing.T) {
	dir := t.TempDir()
	pub, err := auth.Keygen(dir)
	require.NoError(t, err)
	other := t.TempDir()
	_, err = auth.Keygen(other)
	require.NoError(t, err)

	p := filepath.Join(dir, "c.age")
	require.NoError(t, os.WriteFile(p, encryptForTest(t, pub, []byte("secret-payload")), 0o600))
	_, err = auth.DecryptIfArmored(p, filepath.Join(other, "fbm.key"))
	require.Error(t, err)
	require.NotContains(t, err.Error(), "AGE-SECRET-KEY")
}
