package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix/cookies"

	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/auth"
	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/browser"
	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/meta"
	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/state"
)

const (
	// browserProfileDir is the Chromium profile under the state dir. fbm never
	// deletes it, so datr and the device's trust survive between logins.
	browserProfileDir = "browser-profile"
	facebookLoginURL  = "https://www.facebook.com/login"
)

// browserLoginSeam is what tests swap so the flow runs headless, without a
// display, and never loads facebook.com.
type browserLoginSeam struct {
	headless bool // launch headless and skip the display check
	startURL string
	poll     time.Duration
	launch   func(context.Context, browser.LaunchOptions) (*browser.Browser, error)
	loggedIn func(context.Context, *browser.Browser) (*cookies.Cookies, error)
}

var loginSeam = browserLoginSeam{
	startURL: facebookLoginURL,
	poll:     2 * time.Second,
	launch:   browser.Launch,
	loggedIn: loggedIn,
}

// humanLogin is the line that tells the Dot to hand over to the owner.
type humanLogin struct {
	Action   string `json:"action"`
	Where    string `json:"where"`
	TimeoutS int64  `json:"timeout_s"`
}

func hasDisplay() bool {
	return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
}

// loginBrowser is `fbm login --browser`, run under the gate. It keeps a
// cookies.json that still connects; otherwise the owner logs in through a
// Chromium window, and the cookies are kept only if a fresh session connects.
func loginBrowser(ctx context.Context, w io.Writer, st *state.Store, o loginOpts) int {
	if code, done := checkStoredCookies(ctx, w, st, o.proxy); done {
		return code
	}
	if !loginSeam.headless && !hasDisplay() {
		return reportClass(w, "no_display")
	}
	proxy, ok := browserProxy(o.proxy)
	if !ok {
		return reportClass(w, "proxy_auth_unsupported")
	}
	c, code := browserCookies(ctx, w, st, o, proxy)
	if c == nil {
		return code
	}
	return connectLogin(ctx, w, st, c, o)
}

// checkStoredCookies validates an existing cookies.json with a fresh session.
// done means login stops here, with the result printed. Only a missing or
// unusable cookies.json, or Facebook reporting it logged out, sends the owner
// to the browser. The session is closed before it returns, so messagix never
// runs alongside the browser.
func checkStoredCookies(ctx context.Context, w io.Writer, st *state.Store, proxy string) (code int, done bool) {
	if _, _, ok := st.Stat(cookiesFile); !ok {
		return 0, false
	}
	c, err := loadCookies(st)
	if err != nil {
		return 0, false
	}
	s, err := meta.Open(ctx, st, c, proxy, true, state.NewLogger(false))
	if err != nil {
		if ctx.Err() != nil {
			return reportSocket(w, "login", "interrupted", nil), true
		}
		return finishStoredCheck(w, st, nil, c, "permanent_error", meta.Classify(err))
	}
	socket, perr := waitSocket(ctx, s, socketWait)
	return finishStoredCheck(w, st, s, c, socket, perr)
}

// finishStoredCheck closes s and maps the validation result. connected prints
// already_logged_in (0). not_logged_in falls through to the browser. Anything
// else, a transient failure included, prints its class and exits 1 rather than
// putting the owner through a login that would not help; a checkpoint class is
// latched. s may be nil when Open failed.
func finishStoredCheck(w io.Writer, st *state.Store, s *meta.Session, c *cookies.Cookies,
	socket string, perr *meta.ClassifiedError) (code int, done bool) {
	if socket == "connected" {
		closeSession(st, s, c, "login")
		_ = writeJSON(w, map[string]string{"status": "already_logged_in"})
		return 0, true
	}
	s.Close(st)
	if perr != nil && perr.Class == meta.ClassNotLoggedIn {
		return 0, false
	}
	if perr != nil {
		recordCheckpoint(st, "login", perr.Class)
	}
	return reportSocket(w, "login", socket, perr), true
}

// browserProxy turns --proxy into Chromium's scheme://host[:port]. ok is false
// when it carries credentials: Chromium cannot take them on the command line,
// and logging in direct while messagix uses the proxy is an IP jump. A value
// that does not parse is passed through for Launch to refuse.
func browserProxy(proxy string) (server string, ok bool) {
	if proxy == "" {
		return "", true
	}
	u, err := url.Parse(proxy)
	if err != nil {
		return proxy, true
	}
	if u.User != nil {
		return "", false
	}
	return u.Scheme + "://" + u.Host, true
}

// browserCookies launches Chromium on the fbm profile at about:blank, deletes
// any stale c_user and xs, sends the page to the login URL and waits for the
// owner to log in. The browser is closed on every path before it returns. On
// failure c is nil and code is the exit code; the reason is printed.
func browserCookies(ctx context.Context, w io.Writer, st *state.Store, o loginOpts, proxy string) (c *cookies.Cookies, code int) {
	bin, err := browser.FindChromium()
	if err != nil {
		fmt.Fprintf(errOut, "fbm login: %v\n", err)
		return nil, reportClass(w, "browser_launch")
	}
	profile := filepath.Join(st.Dir, browserProfileDir)
	if err := os.MkdirAll(profile, 0o700); err != nil {
		fmt.Fprintf(errOut, "fbm login: create browser profile: %v\n", err)
		return nil, reportClass(w, "browser_launch")
	}
	// MkdirAll leaves a pre-existing directory untouched.
	if err := os.Chmod(profile, 0o700); err != nil {
		fmt.Fprintf(errOut, "fbm login: chmod browser profile: %v\n", err)
		return nil, reportClass(w, "browser_launch")
	}

	// ctx bounds the browser's life, so an interrupt kills it; the login
	// timeout is waitForLogin's own deadline.
	b, err := loginSeam.launch(ctx, browser.LaunchOptions{
		Binary: bin, ProfileDir: profile, AllowNoSandbox: o.allowNoSandbox, Headless: loginSeam.headless, Proxy: proxy,
	})
	if err != nil {
		// Launch errors can name local paths; only the class is printed.
		return nil, browserFailure(ctx, w, "browser_launch")
	}
	// A fresh ctx: after an interrupt the flow's ctx is already done.
	defer func() { _ = b.Close(context.Background()) }()

	if _, err := b.DeleteCookies(ctx, "c_user", "xs"); err != nil {
		return nil, browserFailure(ctx, w, setupClass(err))
	}
	if err := b.Navigate(ctx, loginSeam.startURL); err != nil {
		return nil, browserFailure(ctx, w, setupClass(err))
	}
	_ = writeJSON(w, humanLogin{
		Action:   "human_login",
		Where:    "Chromium window on the Dot computer",
		TimeoutS: int64(o.timeout / time.Second),
	})
	return waitForLogin(ctx, w, b, o.timeout)
}

// waitForLogin polls the browser until auth.FromBrowser accepts its cookies
// and the page has left the login, checkpoint and two-factor flows, on two
// polls in a row, or until the timeout passes, the browser exits or ctx ends.
// Cookies alone are not enough: Facebook can set them mid-2FA or under a
// checkpoint, and a session opened then would trip the checkpoint latch.
func waitForLogin(ctx context.Context, w io.Writer, b *browser.Browser, timeout time.Duration) (*cookies.Cookies, int) {
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	tick := time.NewTicker(loginSeam.poll)
	defer tick.Stop()
	ready := 0
	for {
		select {
		case <-b.Done():
			return nil, browserFailure(ctx, w, "browser_exited")
		case <-pctx.Done():
			return nil, browserFailure(ctx, w, "login_timeout")
		case <-tick.C:
		}
		c, err := loginSeam.loggedIn(pctx, b)
		if errors.Is(err, browser.ErrExited) {
			return nil, browserFailure(ctx, w, "browser_exited")
		}
		if c == nil {
			ready = 0
			continue // a deadline is caught above; any other CDP error is retried
		}
		if ready++; ready >= 2 {
			return c, 0
		}
	}
}

// loggedIn returns the session cookies when the three are present and the
// page is past the login flow, else nil.
func loggedIn(ctx context.Context, b *browser.Browser) (*cookies.Cookies, error) {
	cs, err := b.Cookies(ctx)
	if err != nil {
		return nil, err
	}
	c, err := auth.FromBrowser(cs)
	if err != nil {
		return nil, nil
	}
	u, err := b.CurrentURL(ctx)
	if err != nil {
		return nil, err
	}
	if !pastLogin(u) {
		return nil, nil
	}
	return c, nil
}

// pastLogin reports whether rawURL is Facebook's home feed: an https URL on
// facebook.com or a subdomain, with path "/" or "/home.php". Anything else,
// including every login, checkpoint, two-factor or save-device page, waits.
// An unparseable URL is not past login. The URL is never printed: it can
// carry tokens.
func pastLogin(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host != "facebook.com" && !strings.HasSuffix(host, ".facebook.com") {
		return false
	}
	switch u.Path {
	case "", "/", "/home.php":
		return true
	}
	return false
}

// setupClass is the class for a failed browser step before the owner's turn.
func setupClass(err error) string {
	switch {
	case errors.Is(err, browser.ErrExited):
		return "browser_exited"
	case errors.Is(err, browser.ErrNavigation):
		return "network"
	}
	return "browser_launch"
}

// browserFailure prints class and returns the exit code, unless ctx ended: an
// interrupt also kills the browser, and is reported as an interrupt.
func browserFailure(ctx context.Context, w io.Writer, class string) int {
	if ctx.Err() != nil {
		return reportSocket(w, "login", "interrupted", nil)
	}
	return reportClass(w, class)
}
