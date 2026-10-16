package state_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/auth"
	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/state"
)

func TestOpenCreates0700AndRefusesGitTree(t *testing.T) {
	d := filepath.Join(t.TempDir(), "state")
	s, err := state.Open(d)
	require.NoError(t, err)
	require.Equal(t, d, s.Dir)
	st, err := os.Stat(d)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), st.Mode().Perm())

	g := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(g, ".git"), 0o755))
	target := filepath.Join(g, "sub", "state")
	_, err = state.Open(target)
	require.ErrorIs(t, err, state.ErrInsideGitTree)
	require.Contains(t, err.Error(), g)
	_, statErr := os.Stat(target)
	require.True(t, os.IsNotExist(statErr), "must not create the dir inside a git tree")
}

func TestOpenRefusesGitFileAncestor(t *testing.T) {
	g := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(g, ".git"), []byte("gitdir: elsewhere\n"), 0o644))
	_, err := state.Open(filepath.Join(g, "state"))
	require.ErrorIs(t, err, state.ErrInsideGitTree)
}

func TestOpenTightensExistingDir(t *testing.T) {
	d := filepath.Join(t.TempDir(), "state")
	require.NoError(t, os.Mkdir(d, 0o755))
	_, err := state.Open(d)
	require.NoError(t, err)
	st, err := os.Stat(d)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), st.Mode().Perm())
}

func TestOpenDefaultDir(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_STATE_HOME", xdg)
	s, err := state.Open("")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(xdg, "fbm"), s.Dir)

	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", home)
	s, err = state.Open("")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(home, ".local", "state", "fbm"), s.Dir)
}

func TestWriteSecretIs0600AndAtomic(t *testing.T) {
	s, err := state.Open(filepath.Join(t.TempDir(), "state"))
	require.NoError(t, err)

	require.NoError(t, s.WriteSecret("session.json", []byte("one")))
	require.NoError(t, s.WriteSecret("session.json", []byte("two")))

	got, err := s.ReadSecret("session.json")
	require.NoError(t, err)
	require.Equal(t, []byte("two"), got)

	st, err := os.Stat(filepath.Join(s.Dir, "session.json"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), st.Mode().Perm())

	entries, err := os.ReadDir(s.Dir)
	require.NoError(t, err)
	for _, e := range entries {
		require.False(t, strings.HasSuffix(e.Name(), ".tmp"), "leftover temp file %s", e.Name())
	}
	require.Len(t, entries, 1)
}

func TestWriteSecretRejectsPathSeparators(t *testing.T) {
	s, err := state.Open(filepath.Join(t.TempDir(), "state"))
	require.NoError(t, err)
	for _, name := range []string{"../escape", "a/b", "", ".", ".."} {
		require.Error(t, s.WriteSecret(name, []byte("x")), name)
		_, err := s.ReadSecret(name)
		require.Error(t, err, name)
		require.Error(t, s.Remove(name), name)
	}
}

func TestReadSecretMissing(t *testing.T) {
	s, err := state.Open(filepath.Join(t.TempDir(), "state"))
	require.NoError(t, err)
	_, err = s.ReadSecret("session.json")
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestRemove(t *testing.T) {
	s, err := state.Open(filepath.Join(t.TempDir(), "state"))
	require.NoError(t, err)
	require.NoError(t, s.WriteSecret("cookies.json", []byte("x")))
	require.NoError(t, s.Remove("cookies.json"))
	_, _, ok := s.Stat("cookies.json")
	require.False(t, ok)
	require.NoError(t, s.Remove("cookies.json"), "removing a missing file is not an error")
}

func TestStatReportsShapeOnly(t *testing.T) {
	s, err := state.Open(filepath.Join(t.TempDir(), "state"))
	require.NoError(t, err)

	size, age, ok := s.Stat("session.json")
	require.False(t, ok)
	require.Zero(t, size)
	require.Zero(t, age)

	require.NoError(t, s.WriteSecret("session.json", []byte("12345")))
	size, age, ok = s.Stat("session.json")
	require.True(t, ok)
	require.Equal(t, int64(5), size)
	require.GreaterOrEqual(t, age, time.Duration(0))
	require.Less(t, age, time.Minute)

	old := time.Now().Add(-48 * time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(s.Dir, "session.json"), old, old))
	_, age, ok = s.Stat("session.json")
	require.True(t, ok)
	require.InDelta(t, (48 * time.Hour).Seconds(), age.Seconds(), 60)
}

func TestCoexistsWithAuthKeygen(t *testing.T) {
	s, err := state.Open(filepath.Join(t.TempDir(), "state"))
	require.NoError(t, err)
	_, err = auth.Keygen(s.Dir)
	require.NoError(t, err)
	require.NoError(t, s.WriteSecret("session.json", []byte("x")))

	size, _, ok := s.Stat("fbm.key")
	require.True(t, ok)
	require.Positive(t, size)
	st, err := os.Stat(filepath.Join(s.Dir, "fbm.key"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), st.Mode().Perm())
}

func TestWriteSecretCleansTempOnFailure(t *testing.T) {
	s, err := state.Open(filepath.Join(t.TempDir(), "state"))
	require.NoError(t, err)
	// A non-empty directory at the target makes the final rename fail.
	require.NoError(t, os.MkdirAll(filepath.Join(s.Dir, "session.json", "x"), 0o700))
	require.Error(t, s.WriteSecret("session.json", []byte("data")))
	entries, err := os.ReadDir(s.Dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "session.json", entries[0].Name())
}
