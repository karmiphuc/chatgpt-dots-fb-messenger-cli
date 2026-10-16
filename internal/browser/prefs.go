package browser

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ensurePrefs turns Chromium's password saving off in the profile before
// launch, so the owner's Facebook password never lands in the profile. It
// merges credentials_enable_service=false and
// profile.password_manager_enabled=false into Default/Preferences, keeping
// every other key, and creates the file (0600) and its dirs (0700) if absent.
// A file that is not a JSON object is left as is and reported.
func ensurePrefs(profileDir string) error {
	dir := filepath.Join(profileDir, "Default")
	path := filepath.Join(dir, "Preferences")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("browser Preferences: %w", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("browser Preferences: %w", err)
	}
	prefs := map[string]json.RawMessage{}
	if err == nil {
		if prefs, err = jsonObject(raw); err != nil {
			return fmt.Errorf("browser Preferences %s: %w; left unchanged", path, err)
		}
	}
	profile := map[string]json.RawMessage{}
	if p, ok := prefs["profile"]; ok {
		if profile, err = jsonObject(p); err != nil {
			return fmt.Errorf("browser Preferences %s: profile: %w; left unchanged", path, err)
		}
	}
	off := json.RawMessage("false")
	prefs["credentials_enable_service"] = off
	profile["password_manager_enabled"] = off
	if prefs["profile"], err = json.Marshal(profile); err != nil {
		return fmt.Errorf("browser Preferences: %w", err)
	}
	out, err := json.Marshal(prefs)
	if err != nil {
		return fmt.Errorf("browser Preferences: %w", err)
	}
	if bytes.Equal(out, raw) {
		return nil
	}
	return writeFileAtomic(path, out)
}

// jsonObject decodes raw as a JSON object; null, arrays and scalars are errors.
func jsonObject(raw []byte) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("not a JSON object: %w", err)
	}
	if m == nil {
		return nil, errors.New("not a JSON object")
	}
	return m, nil
}

// writeFileAtomic writes data to a 0600 temp file beside path, then renames it
// over path, so a crash never leaves a half-written file.
func writeFileAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".Preferences-*")
	if err != nil {
		return fmt.Errorf("browser Preferences: %w", err)
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
		return fmt.Errorf("browser Preferences: %w", err)
	}
	return nil
}
