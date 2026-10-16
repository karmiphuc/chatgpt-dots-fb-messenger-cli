package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mau.fi/mautrix-meta/pkg/messagix/cookies"

	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/browser"
	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/meta"
)

func TestNoDisplayFailsBeforeLaunch(t *testing.T) {
	withSeam(t, noLaunch(t))
	noDisplay(t)
	st := testStore(t)
	var out bytes.Buffer
	assert.Equal(t, 1, login(context.Background(), &out, st, loginOpts{browser: true, timeout: time.Minute}))
	assert.JSONEq(t, `{"error_class":"no_display"}`, out.String())
	assert.NoDirExists(t, filepath.Join(st.Dir, browserProfileDir))
}

func TestHasDisplay(t *testing.T) {
	for _, tc := range []struct {
		display, wayland string
		want             bool
	}{{"", "", false}, {":0", "", true}, {"", "wayland-0", true}, {":1", "wayland-0", true}} {
		t.Setenv("DISPLAY", tc.display)
		t.Setenv("WAYLAND_DISPLAY", tc.wayland)
		assert.Equal(t, tc.want, hasDisplay(), "%q %q", tc.display, tc.wayland)
	}
}

// An unusable cookies.json is not a reason to stop: the owner logs in again.
func TestUnusableStoredCookiesFallThroughToBrowserLogin(t *testing.T) {
	withSeam(t, noLaunch(t))
	noDisplay(t)
	for name, content := range map[string]string{"corrupt": "{not json", "incomplete": `{"c_user":"1"}`} {
		st := testStore(t)
		require.NoError(t, st.WriteSecret(cookiesFile, []byte(content)))
		var out bytes.Buffer
		assert.Equal(t, 1, login(context.Background(), &out, st, loginOpts{browser: true, timeout: time.Minute}), name)
		assert.JSONEq(t, `{"error_class":"no_display"}`, out.String(), name)
	}
}

func TestInterruptedValidationStops(t *testing.T) {
	withSeam(t, noLaunch(t))
	noDisplay(t)
	st := testStore(t)
	require.NoError(t, writeCookies(st, testCookies(t)))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	assert.Equal(t, 1, login(ctx, &out, st, loginOpts{browser: true, timeout: time.Minute, proxy: "http://127.0.0.1:1"}))
	assert.Empty(t, out.String(), "interrupted goes to stderr only")
}

// headlessLogin runs the browser flow headless at about:blank: it never
// needs a display and never loads facebook.com.
func headlessLogin(t *testing.T) (bin string) {
	t.Helper()
	bin, err := browser.FindChromium()
	if err != nil {
		t.Skip("no Chromium or Chrome binary found")
	}
	noDisplay(t)
	withSeam(t, func(s *browserLoginSeam) {
		s.headless = true
		s.startURL = "about:blank"
		s.poll = 200 * time.Millisecond
	})
	return bin
}

func profileProcs(profile string) string {
	out, _ := exec.Command("pgrep", "-f", "user-data-dir="+regexp.QuoteMeta(profile)).Output()
	return strings.TrimSpace(string(out))
}

func requireNoBrowser(t *testing.T, profile string) {
	t.Helper()
	var left string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if left = profileProcs(profile); left == "" {
			return
		}
	}
	t.Fatalf("browser processes still running for %s: %s", profile, left)
}

// outputLines splits the command's stdout into JSON objects.
func outputLines(t *testing.T, out string) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(l), &m), l)
		lines = append(lines, m)
	}
	return lines
}

func TestBrowserLoginTimesOut(t *testing.T) {
	headlessLogin(t)
	st := testStore(t)
	profile := filepath.Join(st.Dir, browserProfileDir)

	var out bytes.Buffer
	start := time.Now()
	code := login(context.Background(), &out, st, loginOpts{browser: true, timeout: 3 * time.Second, proxy: "http://127.0.0.1:1"})
	elapsed := time.Since(start)
	assert.Equal(t, 1, code)
	assert.GreaterOrEqual(t, elapsed, 3*time.Second)
	assert.Less(t, elapsed, 20*time.Second)

	lines := outputLines(t, out.String())
	require.Len(t, lines, 2, out.String())
	assert.Equal(t, map[string]any{"action": "human_login", "where": "Chromium window on the Dot computer", "timeout_s": float64(3)}, lines[0])
	assert.Equal(t, map[string]any{"error_class": "login_timeout"}, lines[1])

	requireNoBrowser(t, profile)
	assert.NoFileExists(t, filepath.Join(st.Dir, cookiesFile))
	fi, err := os.Stat(profile)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), fi.Mode().Perm())
}

// firstLine is stdout for a login under test. first is closed once the
// first line, human_login, has been written, so a test acts on the owner's
// turn rather than on a wall-clock guess.
type firstLine struct {
	bytes.Buffer
	once  sync.Once
	first chan struct{}
}

func newFirstLine() *firstLine { return &firstLine{first: make(chan struct{})} }

func (f *firstLine) Write(p []byte) (int, error) {
	n, err := f.Buffer.Write(p)
	if bytes.Contains(p, []byte("\n")) {
		f.once.Do(func() { close(f.first) })
	}
	return n, err
}

func TestBrowserLoginReportsBrowserExit(t *testing.T) {
	headlessLogin(t)
	st := testStore(t)
	profile := filepath.Join(st.Dir, browserProfileDir)
	out := newFirstLine()
	go func() {
		<-out.first
		// A crash on the owner's turn: every process of this profile dies at once.
		_ = exec.Command("pkill", "-KILL", "-f", "user-data-dir="+regexp.QuoteMeta(profile)).Run()
	}()

	code := login(context.Background(), out, st, loginOpts{browser: true, timeout: time.Minute, proxy: "http://127.0.0.1:1"})
	assert.Equal(t, 1, code)
	lines := outputLines(t, out.String())
	require.Len(t, lines, 2, out.String())
	assert.Equal(t, "human_login", lines[0]["action"])
	assert.Equal(t, map[string]any{"error_class": "browser_exited"}, lines[1])
	requireNoBrowser(t, profile)
	assert.NoFileExists(t, filepath.Join(st.Dir, cookiesFile))
}

func TestBrowserLoginInterruptClosesBrowser(t *testing.T) {
	headlessLogin(t)
	st := testStore(t)
	profile := filepath.Join(st.Dir, browserProfileDir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := newFirstLine()
	go func() {
		<-out.first
		cancel()
	}()

	stderr := captureErr(t)
	assert.Equal(t, 1, login(ctx, out, st, loginOpts{browser: true, timeout: time.Minute, proxy: "http://127.0.0.1:1"}))
	lines := outputLines(t, out.String())
	require.Len(t, lines, 1, "interrupted goes to stderr only: %s", out.String())
	assert.Equal(t, "human_login", lines[0]["action"])
	assert.Contains(t, stderr.String(), "interrupted")
	requireNoBrowser(t, profile)
	assert.NoFileExists(t, filepath.Join(st.Dir, cookiesFile))
}

// A refused local connection stands in for a VM with no network.
func TestBrowserLoginNavigateFailureIsNetwork(t *testing.T) {
	headlessLogin(t)
	withSeam(t, func(s *browserLoginSeam) { s.startURL = "http://127.0.0.1:1/" })
	st := testStore(t)
	var out bytes.Buffer
	assert.Equal(t, 1, login(context.Background(), &out, st, loginOpts{browser: true, timeout: time.Minute}))
	assert.JSONEq(t, `{"error_class":"network"}`, out.String(), "no human_login: the page never loaded")
	requireNoBrowser(t, filepath.Join(st.Dir, browserProfileDir))
}

func TestProfileDirFailureIsBrowserLaunch(t *testing.T) {
	headlessLogin(t)
	withSeam(t, noLaunch(t))
	st := testStore(t)
	require.NoError(t, os.WriteFile(filepath.Join(st.Dir, browserProfileDir), nil, 0o600))
	stderr := captureErr(t)
	var out bytes.Buffer
	assert.Equal(t, 1, login(context.Background(), &out, st, loginOpts{browser: true, timeout: time.Minute}))
	assert.JSONEq(t, `{"error_class":"browser_launch"}`, out.String())
	assert.Contains(t, stderr.String(), "browser profile")
}

func TestBrowserProxy(t *testing.T) {
	for in, want := range map[string]string{
		"":                                  "",
		"socks5://127.0.0.1:1080":           "socks5://127.0.0.1:1080",
		"http://proxy.example:3128/":        "http://proxy.example:3128",
		"http://proxy.example:3128/x?y=1#z": "http://proxy.example:3128",
	} {
		got, ok := browserProxy(in)
		assert.True(t, ok, in)
		assert.Equal(t, want, got, in)
	}
	for _, in := range []string{"http://user:pass@proxy.example:3128", "socks5://user@127.0.0.1:1080"} {
		got, ok := browserProxy(in)
		assert.False(t, ok, in)
		assert.Empty(t, got)
	}
}

func TestProxyCredentialsRefusedBeforeLaunch(t *testing.T) {
	headlessLogin(t)
	withSeam(t, noLaunch(t))
	st := testStore(t)
	var out bytes.Buffer
	assert.Equal(t, 1, login(context.Background(), &out, st, loginOpts{browser: true, timeout: time.Minute, proxy: "http://u:s3cret@127.0.0.1:1"}))
	assert.JSONEq(t, `{"error_class":"proxy_auth_unsupported"}`, out.String())
	assert.NoDirExists(t, filepath.Join(st.Dir, browserProfileDir))
}

func TestProxyReachesChromium(t *testing.T) {
	headlessLogin(t)
	var got string
	withSeam(t, func(s *browserLoginSeam) {
		s.launch = func(_ context.Context, o browser.LaunchOptions) (*browser.Browser, error) {
			got = o.Proxy
			return nil, errors.New("stop here")
		}
	})
	var out bytes.Buffer
	login(context.Background(), &out, testStore(t), loginOpts{browser: true, timeout: time.Minute, proxy: "socks5://127.0.0.1:1"})
	assert.Equal(t, "socks5://127.0.0.1:1", got, "the login must leave from the same egress as messagix")
}

func TestPastLogin(t *testing.T) {
	for u, want := range map[string]bool{
		"https://www.facebook.com/":                                  true,
		"https://www.facebook.com":                                   true,
		"https://www.facebook.com/?sk=h_chr":                         true,
		"https://www.facebook.com/home.php":                          true,
		"https://www.facebook.com/home.php?sk=h_chr#x":               true,
		"https://m.facebook.com/":                                    true,
		"https://web.facebook.com/home.php":                          true,
		"https://facebook.com/":                                      true,
		"https://WWW.Facebook.com/":                                  true,
		"https://www.facebook.com/?next=/login":                      true, // query is not the path
		"https://www.facebook.com/login/":                            false,
		"https://www.facebook.com/login.php":                         false,
		"https://www.facebook.com/login/save-device/":                false,
		"https://www.facebook.com/checkpoint/1501092823525282/":      false,
		"https://www.facebook.com/auth_platform/codesubmit/":         false,
		"https://www.facebook.com/auth_platform/afad/":               false,
		"https://www.facebook.com/two_step_verification/two_factor/": false,
		"https://www.facebook.com/settings":                          false,
		"https://www.facebook.com/messages/t/1":                      false,
		"https://www.facebook.com/home.php/x":                        false,
		"about:blank":                                                false,
		"https://www.messenger.com/":                                 false,
		"https://evilfacebook.com/":                                  false,
		"https://www.facebook.com.example.org/":                      false,
		"http://www.facebook.com/":                                   false,
		"%zz":                                                        false,
		"":                                                           false,
	} {
		assert.Equal(t, want, pastLogin(u), u)
	}
}

// The owner's turn ends only after two ready polls in a row; a not-ready poll
// in between starts the count again. The accepting poll cancels ctx so the
// hand-off to messagix fails at once, with no network.
func TestLoginNeedsTwoReadyPollsInARow(t *testing.T) {
	headlessLogin(t)
	// The bound only matters if acceptance comes early: messagix would retry
	// the dead proxy until ctx ends.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ready := testCookies(t)
	script := []bool{true, false, true, true, true}
	calls := 0
	withSeam(t, func(s *browserLoginSeam) {
		s.poll = 20 * time.Millisecond
		s.loggedIn = func(context.Context, *browser.Browser) (*cookies.Cookies, error) {
			ok := script[calls]
			calls++
			if calls == 4 {
				cancel()
			}
			if !ok {
				return nil, nil
			}
			return ready, nil
		}
	})
	st := testStore(t)
	var out bytes.Buffer
	assert.Equal(t, 1, login(ctx, &out, st, loginOpts{browser: true, timeout: time.Minute, proxy: "http://127.0.0.1:1"}))
	assert.Equal(t, 4, calls, "accepted on the second ready poll after the reset")
	lines := outputLines(t, out.String())
	require.Len(t, lines, 2, out.String())
	assert.Equal(t, "human_login", lines[0]["action"])
	assert.Contains(t, lines[1], "error_class", "the hand-off ran and failed without a network")
	requireNoBrowser(t, filepath.Join(st.Dir, browserProfileDir))
	assert.NoFileExists(t, filepath.Join(st.Dir, cookiesFile))
}

func TestFinishStoredCheck(t *testing.T) {
	ce := func(c meta.Class) *meta.ClassifiedError { return &meta.ClassifiedError{Class: c} }
	cases := []struct {
		socket  string
		perr    *meta.ClassifiedError
		done    bool
		code    int
		out     string
		latched string
	}{
		{"connected", nil, true, 0, `{"status":"already_logged_in"}`, ""},
		{"permanent_error", ce(meta.ClassNotLoggedIn), false, 0, ``, ""},
		{"permanent_error", ce(meta.ClassCheckpoint), true, 1, `{"error_class":"checkpoint"}`, "checkpoint"},
		{"permanent_error", ce(meta.ClassRateLimited), true, 1, `{"error_class":"rate_limited"}`, "rate_limited"},
		{"permanent_error", ce(meta.ClassTransient), true, 1, `{"error_class":"transient"}`, ""},
		{"permanent_error", ce(meta.ClassUnknown), true, 1, `{"error_class":"unknown"}`, ""},
		{"timeout", nil, true, 1, `{"error_class":"socket_timeout"}`, ""},
		{"disconnected", nil, true, 1, `{"error_class":"disconnected"}`, ""},
	}
	for _, tc := range cases {
		name := tc.socket
		if tc.perr != nil {
			name += "/" + string(tc.perr.Class)
		}
		for _, s := range []*meta.Session{{}, nil} { // nil: Open itself failed
			if s == nil && tc.socket != "permanent_error" {
				continue
			}
			st := testStore(t)
			var out bytes.Buffer
			code, done := finishStoredCheck(&out, st, s, testCookies(t), tc.socket, tc.perr)
			assert.Equal(t, tc.done, done, name)
			assert.Equal(t, tc.code, code, name)
			if tc.out == "" {
				assert.Empty(t, out.String(), name)
			} else {
				assert.JSONEq(t, tc.out, out.String(), name)
			}
			class, _, ok := st.LastFatal()
			assert.Equal(t, tc.latched != "", ok, name)
			assert.Equal(t, tc.latched, class, name)
		}
	}
}

func TestBrowserLaunchFailureIsClassOnly(t *testing.T) {
	headlessLogin(t)
	withSeam(t, func(s *browserLoginSeam) {
		s.launch = func(ctx context.Context, o browser.LaunchOptions) (*browser.Browser, error) {
			o.Binary = "/nonexistent/chrome-binary"
			return browser.Launch(ctx, o)
		}
	})
	st := testStore(t)
	var out bytes.Buffer
	assert.Equal(t, 1, login(context.Background(), &out, st, loginOpts{browser: true, timeout: time.Minute}))
	assert.JSONEq(t, `{"error_class":"browser_launch"}`, out.String())
}

// After the browser hands over cookies, login drops session.json and keeps
// cookies.json untouched unless the socket connects.
func TestConnectLoginWritesNothingOnFailure(t *testing.T) {
	st := testStore(t)
	require.NoError(t, st.WriteSecret(sessionFile, []byte(`{"Configs":{"BrowserConfigTable":{}}}`)))
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Open fails at once; the dead proxy keeps any request local.

	var out bytes.Buffer
	code := connectLogin(ctx, &out, st, testCookies(t), loginOpts{browser: true, proxy: "http://127.0.0.1:1"})
	assert.Equal(t, 1, code)
	assert.Contains(t, out.String(), "error_class")
	assert.NoFileExists(t, filepath.Join(st.Dir, sessionFile))
	assert.NoFileExists(t, filepath.Join(st.Dir, cookiesFile))
}
