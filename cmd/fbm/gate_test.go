package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/browser"
	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/meta"
	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/state"
)

// withSeam swaps the browser-login seam for one test.
func withSeam(t *testing.T, edit func(*browserLoginSeam)) {
	t.Helper()
	old := loginSeam
	t.Cleanup(func() { loginSeam = old })
	edit(&loginSeam)
}

// noLaunch fails the test if the login flow gets as far as launching a browser.
func noLaunch(t *testing.T) func(*browserLoginSeam) {
	return func(s *browserLoginSeam) {
		s.launch = func(context.Context, browser.LaunchOptions) (*browser.Browser, error) {
			t.Error("browser launched")
			return nil, errors.New("launch not allowed in this test")
		}
	}
}

func noDisplay(t *testing.T) {
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
}

// gatedCommands runs each Meta-contacting command against st. None may get
// past the gate: there is no cookies.json and no export to read.
func gatedCommands(st *state.Store, ownerCleared bool) map[string]func(*bytes.Buffer) int {
	ctx := context.Background()
	return map[string]func(*bytes.Buffer) int{
		"login --browser": func(w *bytes.Buffer) int {
			return login(ctx, w, st, loginOpts{browser: true, timeout: time.Minute, ownerCleared: ownerCleared})
		},
		"login --cookies": func(w *bytes.Buffer) int {
			return login(ctx, w, st, loginOpts{cookies: "/nonexistent/export.json", ownerCleared: ownerCleared})
		},
		"threads": func(w *bytes.Buffer) int { return threads(ctx, w, st, threadsOpts{limit: 1, format: "json"}) },
		"spike":   func(w *bytes.Buffer) int { return spike(ctx, w, st, spikeOpts{hours: 1}) },
	}
}

func TestLatchRefusesEveryMetaCommand(t *testing.T) {
	withSeam(t, noLaunch(t))
	noDisplay(t)
	for _, class := range []string{"checkpoint", "challenge", "suspended", "rate_limited"} {
		st := testStore(t)
		require.NoError(t, st.RecordFatal(class))
		for name, run := range gatedCommands(st, false) {
			var out bytes.Buffer
			assert.Equal(t, 1, run(&out), name)
			assert.JSONEq(t, `{"error_class":"owner_must_clear","last":"`+class+`"}`, out.String(), name)
		}
		got, _, ok := st.LastFatal()
		require.True(t, ok, "the latch stays until the owner clears it")
		assert.Equal(t, class, got)
	}
}

func TestCorruptLatchIsLatched(t *testing.T) {
	withSeam(t, noLaunch(t))
	for name, content := range map[string]string{"garbage": "{not json", "other class": `{"class":"not_logged_in","at":"2026-10-16T00:00:00Z"}`} {
		st := testStore(t)
		require.NoError(t, st.WriteSecret(lastErrorFile, []byte(content)))
		var out bytes.Buffer
		assert.Equal(t, 1, login(context.Background(), &out, st, loginOpts{browser: true, timeout: time.Minute}), name)
		assert.JSONEq(t, `{"error_class":"owner_must_clear","last":"unreadable"}`, out.String(), name)
	}
}

func TestOwnerClearedClearsTheLatch(t *testing.T) {
	withSeam(t, noLaunch(t))
	noDisplay(t)

	st := testStore(t)
	require.NoError(t, st.RecordFatal("checkpoint"))
	var out bytes.Buffer
	assert.Equal(t, 1, login(context.Background(), &out, st, loginOpts{browser: true, timeout: time.Minute, ownerCleared: true}))
	assert.JSONEq(t, `{"error_class":"no_display"}`, out.String(), "past the gate, stopped by the display check")
	_, _, ok := st.LastFatal()
	assert.False(t, ok)
	assert.NoFileExists(t, filepath.Join(st.Dir, lastErrorFile))

	st = testStore(t)
	require.NoError(t, st.WriteSecret(lastErrorFile, []byte("{not json")))
	out.Reset()
	stderr := captureErr(t)
	assert.Equal(t, 1, login(context.Background(), &out, st, loginOpts{cookies: "/nonexistent/export.json", ownerCleared: true}))
	assert.Empty(t, out.String(), "past the gate, the missing export fails on stderr")
	assert.Contains(t, stderr.String(), "/nonexistent/export.json")
	assert.NoFileExists(t, filepath.Join(st.Dir, lastErrorFile), "a corrupt latch is cleared too")
}

func TestBusyLockStopsEveryMetaCommand(t *testing.T) {
	withSeam(t, noLaunch(t))
	st := testStore(t)
	require.NoError(t, st.RecordFatal("checkpoint"))
	unlock, err := st.Lock()
	require.NoError(t, err)
	defer unlock()
	for name, run := range gatedCommands(st, true) {
		var out bytes.Buffer
		assert.Equal(t, 1, run(&out), name)
		assert.JSONEq(t, `{"error_class":"busy"}`, out.String(), name)
	}
	_, _, ok := st.LastFatal()
	assert.True(t, ok, "--owner-cleared does nothing without the lock")
}

func TestGateReleasesTheLock(t *testing.T) {
	withSeam(t, noLaunch(t))
	noDisplay(t)
	st := testStore(t)
	var out bytes.Buffer
	login(context.Background(), &out, st, loginOpts{browser: true, timeout: time.Minute})
	unlock, err := st.Lock()
	require.NoError(t, err, "the command released the lock on return")
	unlock()
}

func TestCheckpointClassesAreRecorded(t *testing.T) {
	recorded := func(t *testing.T, st *state.Store) string {
		class, _, ok := st.LastFatal()
		if !ok {
			return ""
		}
		return class
	}
	cases := map[meta.Class]string{
		meta.ClassCheckpoint:  "checkpoint",
		meta.ClassChallenge:   "challenge",
		meta.ClassSuspended:   "suspended",
		meta.ClassRateLimited: "rate_limited",
		meta.ClassNotLoggedIn: "",
		meta.ClassUnknown:     "",
		meta.ClassTransient:   "",
	}
	for class, want := range cases {
		perr := &meta.ClassifiedError{Class: class}

		st := testStore(t)
		var out bytes.Buffer
		finishLogin(&out, st, &meta.Session{}, testCookies(t), loginOpts{cookies: "/nonexistent"}, "permanent_error", perr)
		assert.Equal(t, want, recorded(t, st), "login %s", class)

		st = testStore(t)
		finishThreads(&out, st, &meta.Session{}, threadsOpts{format: "json"}, "permanent_error", perr)
		assert.Equal(t, want, recorded(t, st), "threads %s", class)

		st = testStore(t)
		v := meta.OpenFailedVerdict(time.Now(), 1, perr)
		finishSpike(&out, st, v, true)
		assert.Equal(t, want, recorded(t, st), "spike %s (outcome %s)", class, v.Outcome)
	}
}

func TestRecordFailureStillExitsNonZero(t *testing.T) {
	st := testStore(t)
	require.NoError(t, os.Mkdir(filepath.Join(st.Dir, lastErrorFile), 0o700)) // blocks the rename
	stderr := captureErr(t)
	var out bytes.Buffer
	code := finishLogin(&out, st, &meta.Session{}, testCookies(t), loginOpts{cookies: "/nonexistent"},
		"permanent_error", &meta.ClassifiedError{Class: meta.ClassCheckpoint})
	assert.Equal(t, 1, code)
	assert.JSONEq(t, `{"error_class":"checkpoint"}`, out.String())
	assert.Contains(t, stderr.String(), "checkpoint not recorded")
}
