package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/browser"
	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/state"
)

var doctorKeys = []string{
	"arch", "chromium", "display", "go_version", "isolation", "last_fatal",
	"logged_in", "shm_mb", "state_dir", "sudo", "systemd_user", "uid",
}

var launchKeys = []string{"launch_ok", "needs_no_sandbox", "sandbox_ok"}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func runDoctorJSON(t *testing.T, args ...string) (map[string]any, string) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	return runDoctorIn(t, args...)
}

func runDoctorIn(t *testing.T, args ...string) (map[string]any, string) {
	t.Helper()
	var out bytes.Buffer
	require.Equal(t, 0, doctor(context.Background(), &out, args))
	var m map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &m), out.String())
	return m, out.String()
}

func TestDoctorEnvKeySet(t *testing.T) {
	m, _ := runDoctorJSON(t, "--env")
	assert.Equal(t, doctorKeys, sortedKeys(m))
	assert.Equal(t, "advisory", m["isolation"])
	assert.Equal(t, false, m["logged_in"])
	assert.Nil(t, m["last_fatal"])
	assert.Contains(t, []any{"none", "nopasswd"}, m["sudo"])
	assert.IsType(t, true, m["display"])
	assert.IsType(t, true, m["systemd_user"])
	assert.EqualValues(t, os.Getuid(), m["uid"])
}

func TestDoctorEnvWorksOnEmptyStateDir(t *testing.T) {
	dir := t.TempDir() + "/not/yet/created"
	t.Setenv("XDG_STATE_HOME", dir)
	m, _ := runDoctorIn(t, "--env")
	assert.Equal(t, dir+"/fbm", m["state_dir"])
	assert.Equal(t, false, m["logged_in"])
}

func TestDoctorEnvReportsStateWithoutSecrets(t *testing.T) {
	const secret = "FAKE-SECRET-VALUE-9f3a1c"
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", root)
	st, err := state.Open(root + "/fbm")
	require.NoError(t, err)
	for _, name := range []string{cookiesFile, sessionFile, keyFile} {
		require.NoError(t, st.WriteSecret(name, []byte(`{"xs":"`+secret+`","c_user":"`+secret+`","datr":"`+secret+`"}`)))
	}
	require.NoError(t, st.RecordFatal("checkpoint"))

	m, raw := runDoctorIn(t, "--env")
	assert.Equal(t, true, m["logged_in"])
	assert.Equal(t, "checkpoint", m["last_fatal"])
	assert.NotContains(t, raw, secret)
}

func TestDoctorLaunchCheckKeySet(t *testing.T) {
	if _, err := browser.FindChromium(); err != nil {
		t.Skip("no Chromium or Chrome binary found")
	}
	m, _ := runDoctorJSON(t, "--env", "--launch-check")
	want := append(append([]string{}, doctorKeys...), launchKeys...)
	sort.Strings(want)
	assert.Equal(t, want, sortedKeys(m))
	for _, k := range launchKeys {
		assert.IsType(t, true, m[k], k)
	}
	if m["sandbox_ok"] == true {
		assert.Equal(t, true, m["launch_ok"])
		assert.Equal(t, false, m["needs_no_sandbox"])
	}
}

func TestDoctorFlagErrors(t *testing.T) {
	captureErr(t)
	for _, bad := range [][]string{
		nil,
		{"--launch-check"},
		{"--env", "extra"},
		{"--bogus"},
	} {
		var out bytes.Buffer
		assert.Equal(t, 2, doctor(context.Background(), &out, bad), "%v", bad)
		assert.Empty(t, out.String(), "%v", bad)
	}
}

func TestDiagnoseLaunch(t *testing.T) {
	boom := errors.New("boom")
	cases := []struct {
		name string
		sb   error
		nsb  error
		want launchResult
	}{
		{"sandboxed ok", nil, nil, launchResult{OK: true, SandboxOK: true}},
		{"sandbox fails, no-sandbox works", boom, nil, launchResult{OK: true, NeedsNoSandbox: true}},
		{"both fail", boom, boom, launchResult{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls []bool
			got := diagnoseLaunch(func(noSandbox bool) error {
				calls = append(calls, noSandbox)
				if noSandbox {
					return tc.nsb
				}
				return tc.sb
			})
			assert.Equal(t, tc.want, got)
			require.NotEmpty(t, calls)
			assert.False(t, calls[0], "sandboxed attempt first")
			if tc.sb == nil {
				assert.Len(t, calls, 1, "no retry after a sandboxed success")
			}
		})
	}
}

func TestDoctorLastFatalIsAllowlisted(t *testing.T) {
	const secret = "FAKE-SECRET-VALUE-7d21e0"
	for _, class := range []string{"checkpoint", "challenge", "suspended", "rate_limited"} {
		assert.Equal(t, class, reportableFatal(class))
	}
	assert.Equal(t, "unreadable", reportableFatal("unreadable"))
	// fbm never records not_logged_in, so finding it means someone else wrote it.
	assert.Equal(t, "unreadable", reportableFatal("not_logged_in"))

	for name, content := range map[string]string{
		"arbitrary class": `{"class":"` + secret + `","at":"2026-01-01T00:00:00Z"}`,
		"not json":        secret,
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("XDG_STATE_HOME", root)
			st, err := state.Open(root + "/fbm")
			require.NoError(t, err)
			require.NoError(t, st.WriteSecret(lastErrorFile, []byte(content)))
			m, raw := runDoctorIn(t, "--env")
			assert.Equal(t, "unreadable", m["last_fatal"])
			assert.NotContains(t, raw, secret)
		})
	}
}

// Each launch attempt gets its own budget: a sandboxed attempt that runs out
// its time must not leave the no-sandbox diagnosis with an expired context.
func TestLaunchAttemptsHaveSeparateBudgets(t *testing.T) {
	old, oldAttempt := launchAttemptTimeout, launchAttempt
	t.Cleanup(func() { launchAttemptTimeout, launchAttempt = old, oldAttempt })
	launchAttemptTimeout = 100 * time.Millisecond
	launchAttempt = func(ctx context.Context, _ string, noSandbox, _ bool) error {
		if !noSandbox {
			<-ctx.Done()
			return ctx.Err()
		}
		return ctx.Err()
	}
	rep := doctorReport{Chromium: &chromiumInfo{Path: "unused"}}
	applyLaunchCheck(context.Background(), &rep)
	assert.Equal(t, launchResult{OK: true, NeedsNoSandbox: true},
		launchResult{OK: *rep.LaunchOK, SandboxOK: *rep.SandboxOK, NeedsNoSandbox: *rep.NeedsNoSandbox})
}

// Cancelling the context while a launched browser is up (a SIGTERM reaches
// doctor this way) must kill it and remove the throwaway profile.
func TestTryLaunchCancelledCleansUp(t *testing.T) {
	bin, err := browser.FindChromium()
	if err != nil {
		t.Skip("no Chromium or Chrome binary found")
	}
	old := browserLaunch
	t.Cleanup(func() { browserLaunch = old })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var profile string
	browserLaunch = func(c context.Context, o browser.LaunchOptions) (*browser.Browser, error) {
		profile = o.ProfileDir
		b, err := old(c, o)
		cancel() // the signal lands once the browser is up
		return b, err
	}
	assert.Error(t, tryLaunch(ctx, bin, false, true))
	require.NotEmpty(t, profile)
	_, err = os.Stat(profile)
	assert.ErrorIs(t, err, os.ErrNotExist, "profile removed")
	requireNoBrowser(t, profile)
}
