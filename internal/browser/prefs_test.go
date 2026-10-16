package browser_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/browser"
)

func readPrefs(t *testing.T, profile string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(profile, "Default", "Preferences"))
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	return m
}

func requirePasswordSavingOff(t *testing.T, m map[string]any) {
	t.Helper()
	require.Equal(t, false, m["credentials_enable_service"])
	profile, ok := m["profile"].(map[string]any)
	require.True(t, ok, "profile must be an object")
	require.Equal(t, false, profile["password_manager_enabled"])
}

func TestEnsurePrefsCreatesPreferences(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "browser-profile")
	require.NoError(t, browser.EnsurePrefs(profile))
	requirePasswordSavingOff(t, readPrefs(t, profile))
	for p, want := range map[string]os.FileMode{
		profile:                           0o700,
		filepath.Join(profile, "Default"): 0o700,
		filepath.Join(profile, "Default", "Preferences"): 0o600,
	} {
		fi, err := os.Stat(p)
		require.NoError(t, err)
		require.Equal(t, want, fi.Mode().Perm(), p)
	}
}

func TestEnsurePrefsKeepsOtherKeys(t *testing.T) {
	profile := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(profile, "Default"), 0o700))
	prefs := filepath.Join(profile, "Default", "Preferences")
	require.NoError(t, os.WriteFile(prefs, []byte(`{"credentials_enable_service":true,"intl":{"accept_languages":"en-US"},`+
		`"profile":{"name":"Person 1","password_manager_enabled":true,"exit_type":"Normal"},"big":12345678901234567890}`), 0o600))
	require.NoError(t, browser.EnsurePrefs(profile))
	m := readPrefs(t, profile)
	requirePasswordSavingOff(t, m)
	require.Equal(t, map[string]any{"accept_languages": "en-US"}, m["intl"])
	require.Equal(t, "Person 1", m["profile"].(map[string]any)["name"])
	require.Equal(t, "Normal", m["profile"].(map[string]any)["exit_type"])
	raw, err := os.ReadFile(prefs)
	require.NoError(t, err)
	require.Contains(t, string(raw), "12345678901234567890", "numbers pass through unrounded")

	// A second run finds nothing to change.
	require.NoError(t, browser.EnsurePrefs(profile))
	again, err := os.ReadFile(prefs)
	require.NoError(t, err)
	require.Equal(t, raw, again)
}

func TestEnsurePrefsRefusesInvalidJSON(t *testing.T) {
	for _, body := range []string{`{"profile":`, `null`, `[]`, `{"profile":"x"}`} {
		profile := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(profile, "Default"), 0o700))
		prefs := filepath.Join(profile, "Default", "Preferences")
		require.NoError(t, os.WriteFile(prefs, []byte(body), 0o644))
		require.Error(t, browser.EnsurePrefs(profile), body)
		raw, err := os.ReadFile(prefs)
		require.NoError(t, err)
		require.Equal(t, body, string(raw), "file untouched")
		fi, err := os.Stat(prefs)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o644), fi.Mode().Perm(), "mode untouched")
		entries, err := os.ReadDir(filepath.Join(profile, "Default"))
		require.NoError(t, err)
		require.Len(t, entries, 1, "no temp file left behind")
	}
}

// Launch turns password saving off before Chromium starts, and refuses to
// start on a Preferences file it cannot merge.
func TestLaunchEnsuresPrefsBeforeStart(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "p")
	_, err := browser.Launch(context.Background(), browser.LaunchOptions{Binary: "/nonexistent", ProfileDir: profile})
	require.ErrorContains(t, err, "/nonexistent", "fails at start, after prefs")
	requirePasswordSavingOff(t, readPrefs(t, profile))

	bad := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(bad, "Default"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(bad, "Default", "Preferences"), []byte("{"), 0o600))
	_, err = browser.Launch(context.Background(), browser.LaunchOptions{Binary: "/nonexistent", ProfileDir: bad})
	require.ErrorContains(t, err, "Preferences")
	require.NotContains(t, err.Error(), "/nonexistent", "never started")
}
