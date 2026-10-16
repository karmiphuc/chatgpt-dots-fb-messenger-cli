package browser_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/browser"
)

// launch starts a headless browser on a fresh profile at about:blank. Cleanup
// closes it and asserts no process with that profile is left running.
func launch(t *testing.T) *browser.Browser {
	t.Helper()
	bin, err := browser.FindChromium()
	if err != nil {
		t.Skip("no Chromium or Chrome binary found")
	}
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	b, err := browser.Launch(ctx, browser.LaunchOptions{Binary: bin, ProfileDir: dir, Headless: true})
	require.NoError(t, err)
	require.NotEmpty(t, pgrep(dir), "pgrep pattern must match the running browser")
	t.Cleanup(func() {
		_ = b.Close(context.Background())
		requireNoChrome(t, dir)
	})
	return b
}

func pgrep(profile string) string {
	out, _ := exec.Command("pgrep", "-f", "user-data-dir="+regexp.QuoteMeta(profile)).Output()
	return strings.TrimSpace(string(out))
}

// requireNoChrome polls briefly, because helper processes are reaped
// asynchronously after the browser process exits.
func requireNoChrome(t *testing.T, profile string) {
	t.Helper()
	var left string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if left = pgrep(profile); left == "" {
			return
		}
	}
	t.Fatalf("chrome processes still running for %s: %s", profile, left)
}

func names(cs []browser.Cookie) []string {
	var n []string
	for _, c := range cs {
		n = append(n, c.Name+"@"+c.Domain)
	}
	sort.Strings(n)
	return n
}

func TestCookiesKeepsOnlyMetaNamesOnFacebook(t *testing.T) {
	b := launch(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	require.NoError(t, b.SetCookiesForTest(ctx, []browser.Cookie{
		{Name: "c_user", Value: "1", Domain: ".facebook.com", Path: "/"},
		{Name: "xs", Value: "fake-xs", Domain: ".facebook.com", Path: "/"},
		{Name: "datr", Value: "fake-datr", Domain: ".facebook.com", Path: "/"},
		{Name: "fr", Value: "fake-fr", Domain: ".facebook.com", Path: "/"},
		{Name: "sid", Value: "x", Domain: ".example.com", Path: "/"},
		{Name: "c_user", Value: "2", Domain: ".example.com", Path: "/"},
	}))
	cs, err := b.Cookies(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"c_user@.facebook.com", "datr@.facebook.com", "xs@.facebook.com"}, names(cs))
	for _, c := range cs {
		require.Equal(t, "/", c.Path)
	}
}

func TestDeleteCookiesRemovesEveryVariant(t *testing.T) {
	b := launch(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	require.NoError(t, b.SetCookiesForTest(ctx, []browser.Cookie{
		{Name: "c_user", Value: "1", Domain: ".facebook.com", Path: "/"},
		{Name: "xs", Value: "fake-xs", Domain: ".facebook.com", Path: "/"},
		{Name: "c_user", Value: "1", Domain: "www.facebook.com", Path: "/"},
		{Name: "xs", Value: "fake-xs-host", Domain: "www.facebook.com", Path: "/"},
		{Name: "datr", Value: "fake-datr", Domain: ".facebook.com", Path: "/"},
		{Name: "xs", Value: "fake-xs-part", Domain: ".facebook.com", Path: "/",
			PartitionKey: json.RawMessage(`{"topLevelSite":"https://example.com","hasCrossSiteAncestor":false}`)},
		{Name: "xs", Value: "x", Domain: ".example.com", Path: "/"},
	}))
	before, err := b.Cookies(ctx)
	require.NoError(t, err)
	require.Len(t, before, 6, "domain, host-only and partitioned variants must all be planted")

	_, err = b.DeleteCookies(ctx, "c_user", "xs", "c_usr")
	require.ErrorContains(t, err, "c_usr")
	again, err := b.Cookies(ctx)
	require.NoError(t, err)
	require.Len(t, again, 6, "an unknown name must fail before anything is deleted")

	n, err := b.DeleteCookies(ctx, "c_user", "xs")
	require.NoError(t, err)
	require.Equal(t, 5, n)

	after, err := b.Cookies(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"datr@.facebook.com"}, names(after))
}

func TestNavigateAboutBlank(t *testing.T) {
	b := launch(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	require.NoError(t, b.Navigate(ctx, "about:blank#x"))
}

func TestCloseExitsAndClosesDone(t *testing.T) {
	b := launch(t)
	start := time.Now()
	require.NoError(t, b.Close(context.Background()))
	require.Less(t, time.Since(start), 10*time.Second)
	require.NoError(t, b.Close(context.Background()), "a second Close is a no-op")
	select {
	case <-b.Done():
	default:
		t.Fatal("Done not closed after Close")
	}
}

func TestKilledBrowserReturnsErrExited(t *testing.T) {
	b := launch(t)
	require.NoError(t, b.KillForTest())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	_, err := b.Cookies(ctx)
	require.ErrorIs(t, err, browser.ErrExited)
	require.Less(t, time.Since(start), 5*time.Second)
	select {
	case <-b.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done not closed after kill")
	}
}

func TestCancelledCallReturnsPromptly(t *testing.T) {
	b := launch(t)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	start := time.Now()
	// A promise that never settles keeps the call pending until ctx ends.
	err := b.CallForTest(ctx, "Runtime.evaluate", map[string]any{
		"expression":   "new Promise(() => {})",
		"awaitPromise": true,
	})
	require.ErrorIs(t, err, context.Canceled)
	require.Less(t, time.Since(start), 2*time.Second)

	// The connection stays usable after an abandoned call.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()
	_, err = b.Cookies(ctx2)
	require.NoError(t, err)
}

func TestFindChromiumHonoursOverride(t *testing.T) {
	t.Setenv("FBM_CHROMIUM", "/nonexistent/chrome")
	_, err := browser.FindChromium()
	require.ErrorContains(t, err, "FBM_CHROMIUM")
}

func TestCookieKeepsPartitionKeyOpaque(t *testing.T) {
	var c browser.Cookie
	raw := `{"name":"xs","value":"v","domain":".facebook.com","path":"/","expires":-1,"session":true,` +
		`"partitionKey":{"topLevelSite":"https://facebook.com","hasCrossSiteAncestor":false}}`
	require.NoError(t, json.Unmarshal([]byte(raw), &c))
	require.JSONEq(t, `{"topLevelSite":"https://facebook.com","hasCrossSiteAncestor":false}`, string(c.PartitionKey))
	require.True(t, c.Session)
}

func TestCancelledLaunchContextKillsBrowser(t *testing.T) {
	bin, err := browser.FindChromium()
	if err != nil {
		t.Skip("no Chromium or Chrome binary found")
	}
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b, err := browser.Launch(ctx, browser.LaunchOptions{Binary: bin, ProfileDir: dir, Headless: true})
	require.NoError(t, err)
	cancel()
	select {
	case <-b.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("browser still running after its context was cancelled")
	}
	requireNoChrome(t, dir)
}

func TestLaunchFailsWhenBinaryIsNotABrowser(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bin, err := exec.LookPath("true")
	require.NoError(t, err)
	_, err = browser.Launch(ctx, browser.LaunchOptions{Binary: bin, ProfileDir: t.TempDir()})
	require.ErrorIs(t, err, browser.ErrExited)
}

func TestLaunchRejectsUnsafeProfileDir(t *testing.T) {
	for _, dir := range []string{"", "relative/profile"} {
		_, err := browser.Launch(context.Background(), browser.LaunchOptions{Binary: "/nonexistent", ProfileDir: dir})
		require.ErrorContains(t, err, "profile dir", "dir %q", dir)
	}
}

func TestBuildArgs(t *testing.T) {
	for _, tc := range []struct {
		headless, noSandbox bool
		proxy               string
	}{{false, false, ""}, {true, false, "socks5://127.0.0.1:1080"}, {false, true, "http://proxy.example:3128"}, {true, true, ""}} {
		args := browser.BuildArgs(browser.LaunchOptions{ProfileDir: "/p", Headless: tc.headless, AllowNoSandbox: tc.noSandbox, Proxy: tc.proxy})
		for _, a := range args {
			require.False(t, strings.HasPrefix(a, "--remote-debugging-port"), a)
		}
		require.Contains(t, args, "--remote-debugging-pipe")
		require.Contains(t, args, "--user-data-dir=/p")
		require.Equal(t, tc.headless, slices.Contains(args, "--headless=new"))
		require.Equal(t, tc.noSandbox, slices.Contains(args, "--no-sandbox"))
		proxyArgs := slices.DeleteFunc(slices.Clone(args), func(a string) bool { return !strings.HasPrefix(a, "--proxy-server") })
		if tc.proxy == "" {
			require.Empty(t, proxyArgs)
		} else {
			require.Equal(t, []string{"--proxy-server=" + tc.proxy}, proxyArgs)
		}
		require.Contains(t, args, "--password-store=basic")
		require.Equal(t, "about:blank", args[len(args)-1])
	}
}

func TestLaunchRejectsProxyWithCredentialsOrPath(t *testing.T) {
	for _, p := range []string{"http://user:s3cret@proxy.example:3128", "http://user@proxy.example:3128",
		"http://proxy.example:3128/path", "http://proxy.example:3128/", "proxy.example:3128", "http://proxy.example:3128?x=1"} {
		_, err := browser.Launch(context.Background(), browser.LaunchOptions{Binary: "/nonexistent", ProfileDir: t.TempDir(), Proxy: p})
		require.ErrorContains(t, err, "proxy must be", p)
		require.NotContains(t, err.Error(), "s3cret")
	}
	for _, p := range []string{"socks5h://127.0.0.1:1080", "ftp://proxy.example:21"} {
		_, err := browser.Launch(context.Background(), browser.LaunchOptions{Binary: "/nonexistent", ProfileDir: t.TempDir(), Proxy: p})
		require.ErrorContains(t, err, "proxy scheme", p)
	}
}

func TestCurrentURL(t *testing.T) {
	b := launch(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	u, err := b.CurrentURL(ctx)
	require.NoError(t, err)
	require.Equal(t, "about:blank", u)
	require.NoError(t, b.Navigate(ctx, "about:blank#signed-in"))
	u, err = b.CurrentURL(ctx)
	require.NoError(t, err)
	require.Equal(t, "about:blank#signed-in", u)
}

// A refused local connection stands in for no network; nothing leaves the host.
func TestNavigateFailureWrapsErrNavigation(t *testing.T) {
	b := launch(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err := b.Navigate(ctx, "http://127.0.0.1:1/")
	require.ErrorIs(t, err, browser.ErrNavigation)
	require.NotErrorIs(t, err, browser.ErrExited)
}

func TestCookieFormattingRedactsValue(t *testing.T) {
	c := browser.Cookie{Name: "xs", Value: "s3cret-xs", Domain: ".facebook.com", Path: "/"}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
		out := fmt.Sprintf(verb, c)
		require.NotContains(t, out, "s3cret", verb)
		require.Contains(t, out, "xs@.facebook.com/", verb)
	}
	out := fmt.Sprintf("%v %+v", []browser.Cookie{c}, &c)
	require.NotContains(t, out, "s3cret")
	require.Equal(t, "xs@.facebook.com/=<redacted len 9>", c.String())
}

func TestAttachTimeoutKillsUnresponsiveBrowser(t *testing.T) {
	defer browser.SetAttachTimeout(500 * time.Millisecond)()
	// A "browser" that stays alive but never answers on fd 4.
	bin := filepath.Join(t.TempDir(), "fake-chrome")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nsleep 60\n"), 0o755))
	dir := t.TempDir()
	start := time.Now()
	_, err := browser.Launch(context.Background(), browser.LaunchOptions{Binary: bin, ProfileDir: dir})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), 5*time.Second)
	requireNoChrome(t, dir)
}

func TestFindChromiumChecksArm64InstallPath(t *testing.T) {
	home := t.TempDir()
	p := filepath.Join(home, ".local/share/fbm/chrome-linux-arm64/chrome")
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755))
	t.Setenv("HOME", home)
	t.Setenv("FBM_CHROMIUM", "")
	got, err := browser.FindChromium()
	require.NoError(t, err)
	require.Equal(t, p, got)
}
