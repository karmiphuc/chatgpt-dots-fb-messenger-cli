package state_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/state"
)

func TestFatalRoundTrip(t *testing.T) {
	s, err := state.Open(filepath.Join(t.TempDir(), "state"))
	require.NoError(t, err)

	_, _, ok := s.LastFatal()
	require.False(t, ok, "no file means not latched")
	require.NoError(t, s.ClearFatal(), "clearing nothing is not an error")

	before := time.Now().Add(-time.Second)
	require.NoError(t, s.RecordFatal("checkpoint"))
	fi, err := os.Stat(filepath.Join(s.Dir, "last_error.json"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), fi.Mode().Perm())

	class, at, ok := s.LastFatal()
	require.True(t, ok)
	require.Equal(t, "checkpoint", class)
	require.True(t, at.After(before) && at.Before(time.Now().Add(time.Second)), "at is the record time")

	require.NoError(t, s.ClearFatal())
	_, _, ok = s.LastFatal()
	require.False(t, ok)
	require.NoFileExists(t, filepath.Join(s.Dir, "last_error.json"))
}

func TestLastFatalUnreadableFileIsReported(t *testing.T) {
	for name, content := range map[string]string{
		"not json":    "not json",
		"empty class": `{"class":"","at":"2026-10-16T00:00:00Z"}`,
		"bad time":    `{"class":"checkpoint","at":"yesterday"}`,
		"empty file":  "",
	} {
		t.Run(name, func(t *testing.T) {
			s, err := state.Open(filepath.Join(t.TempDir(), "state"))
			require.NoError(t, err)
			require.NoError(t, s.WriteSecret("last_error.json", []byte(content)))
			class, _, ok := s.LastFatal()
			require.True(t, ok, "a corrupt latch must still count as present")
			require.Equal(t, state.UnreadableFatal, class)
		})
	}
	t.Run("not a regular file", func(t *testing.T) {
		s, err := state.Open(filepath.Join(t.TempDir(), "state"))
		require.NoError(t, err)
		require.NoError(t, os.Mkdir(filepath.Join(s.Dir, "last_error.json"), 0o700))
		class, _, ok := s.LastFatal()
		require.True(t, ok)
		require.Equal(t, state.UnreadableFatal, class)
	})
}

func TestLockIsExclusive(t *testing.T) {
	s, err := state.Open(filepath.Join(t.TempDir(), "state"))
	require.NoError(t, err)

	unlock, err := s.Lock()
	require.NoError(t, err)
	fi, err := os.Stat(filepath.Join(s.Dir, "fbm.lock"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), fi.Mode().Perm())

	// flock is per open file, so a second open in this process contends too.
	_, err = s.Lock()
	require.ErrorIs(t, err, state.ErrBusy)

	unlock()
	unlock2, err := s.Lock()
	require.NoError(t, err, "the lock is free again after unlock")
	unlock2()
	require.FileExists(t, filepath.Join(s.Dir, "fbm.lock"), "the lock file is never removed")
}
