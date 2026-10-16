// Package browser launches Chromium on a dedicated profile so the owner can log
// in to Facebook, then reads the c_user, xs and datr cookies over CDP. CDP runs
// only over --remote-debugging-pipe; no TCP debugging port is ever opened.
package browser

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// ErrExited is wrapped by every call made after the browser process is gone.
var ErrExited = errors.New("browser exited")

// ErrNavigation is wrapped by Navigate when the page could not load the URL,
// e.g. no network or a refused connection, as opposed to a CDP failure.
var ErrNavigation = errors.New("navigation failed")

// closeTimeout is how long Close waits for a clean exit before killing.
const closeTimeout = 10 * time.Second

// attachTimeout bounds how long Launch waits for a page target to answer CDP.
// A var so tests can shorten it.
var attachTimeout = 30 * time.Second

// LaunchOptions configures Launch. The browser always starts at about:blank.
type LaunchOptions struct {
	Binary         string
	ProfileDir     string
	AllowNoSandbox bool // adds --no-sandbox; only on the owner's explicit approval
	Headless       bool
	// Proxy is scheme://host[:port] for --proxy-server, so the login comes from
	// the same egress IP as the messagix session. Credentials are not supported.
	Proxy string
}

// Browser is a running Chromium attached to its first page target.
type Browser struct {
	cmd     *exec.Cmd
	conn    *conn
	target  string // targetId of the attached page
	session string // flattened sessionId of the page target
	exited  chan struct{}

	// The group kill runs at most once: after the browser is reaped its pgid
	// may be reused, so a later kill could hit an unrelated group.
	killOnce sync.Once
}

// Launch starts Chromium in its own process group (on Linux, one the kernel
// kills if fbm dies) and attaches to the first page target. ctx bounds the
// browser's whole life: when it ends, the process group is killed. Attaching
// is bounded by attachTimeout. On any launch error the browser is killed
// before returning. ProfileDir must be absolute, so an empty value can never
// fall back to the owner's default profile.
func Launch(ctx context.Context, opts LaunchOptions) (*Browser, error) {
	if opts.ProfileDir == "" || !filepath.IsAbs(opts.ProfileDir) {
		return nil, fmt.Errorf("launch browser: profile dir %q must be an absolute path", opts.ProfileDir)
	}
	if err := checkProxy(opts.Proxy); err != nil {
		return nil, fmt.Errorf("launch browser: %w", err)
	}
	if err := ensurePrefs(opts.ProfileDir); err != nil {
		return nil, fmt.Errorf("launch browser: %w", err)
	}

	// Chromium reads commands on fd 3 and writes responses on fd 4.
	cmdR, cmdW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("launch browser: %w", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		cmdR.Close()
		cmdW.Close()
		return nil, fmt.Errorf("launch browser: %w", err)
	}

	cmd := exec.CommandContext(ctx, opts.Binary, buildArgs(opts)...)
	b := &Browser{cmd: cmd, exited: make(chan struct{})}
	cmd.ExtraFiles = []*os.File{cmdR, outW}
	cmd.SysProcAttr = sysProcAttr()
	cmd.Cancel = b.killGroup
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Start(); err != nil {
		for _, f := range []*os.File{cmdR, cmdW, outR, outW} {
			f.Close()
		}
		return nil, fmt.Errorf("launch browser %s: %w", opts.Binary, err)
	}
	// Only the child may hold its ends, so its exit shows up as EOF and EPIPE.
	cmdR.Close()
	outW.Close()

	b.conn = newConn(cmdW, outR, b.exited)
	go func() {
		_ = cmd.Wait()
		close(b.exited)
		cmdW.Close()
		outR.Close()
	}()

	actx, cancel := context.WithTimeout(ctx, attachTimeout)
	defer cancel()
	if err := b.attach(actx); err != nil {
		b.kill()
		if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("launch browser: no CDP page target within %s: %w", attachTimeout, err)
		}
		return nil, fmt.Errorf("launch browser: %w", err)
	}
	return b, nil
}

// buildArgs is the Chromium command line. It never holds a TCP debugging port.
func buildArgs(opts LaunchOptions) []string {
	args := []string{
		"--remote-debugging-pipe",
		"--user-data-dir=" + opts.ProfileDir,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-background-networking",
		"--disable-sync",
		"--disable-extensions",
		// Keep the profile's secrets out of the desktop keyring, which an
		// unattended session may not have unlocked.
		"--password-store=basic",
	}
	if opts.Headless {
		args = append(args, "--headless=new")
	}
	if opts.AllowNoSandbox {
		args = append(args, "--no-sandbox")
	}
	if opts.Proxy != "" {
		args = append(args, "--proxy-server="+opts.Proxy)
	}
	return append(args, "about:blank")
}

// checkProxy accepts an empty proxy or exactly scheme://host[:port] with a
// scheme Chromium understands. Anything else is refused rather than passed on,
// because Chromium may ignore a proxy it cannot parse and connect direct. The
// error never echoes the value, which could hold credentials.
func checkProxy(proxy string) error {
	if proxy == "" {
		return nil
	}
	u, err := url.Parse(proxy)
	if err != nil || u.Host == "" || proxy != u.Scheme+"://"+u.Host {
		return errors.New("proxy must be scheme://host[:port] with no credentials")
	}
	switch u.Scheme {
	case "http", "https", "socks4", "socks5":
		return nil
	}
	return errors.New("proxy scheme must be http, https, socks4 or socks5")
}

// attach waits for the first page target and attaches to it with flatten, so
// page commands travel on the same pipe tagged with its sessionId.
func (b *Browser) attach(ctx context.Context) error {
	for {
		var res struct {
			TargetInfos []struct {
				TargetID string `json:"targetId"`
				Type     string `json:"type"`
			} `json:"targetInfos"`
		}
		if err := b.conn.call(ctx, "", "Target.getTargets", nil, &res); err != nil {
			return err
		}
		for _, t := range res.TargetInfos {
			if t.Type != "page" {
				continue
			}
			var att struct {
				SessionID string `json:"sessionId"`
			}
			params := map[string]any{"targetId": t.TargetID, "flatten": true}
			if err := b.conn.call(ctx, "", "Target.attachToTarget", params, &att); err != nil {
				return err
			}
			b.target, b.session = t.TargetID, att.SessionID
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for page target: %w", ctx.Err())
		case <-b.exited:
			return fmt.Errorf("wait for page target: %w", ErrExited)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// Navigate loads url in the attached page.
func (b *Browser) Navigate(ctx context.Context, url string) error {
	var res struct {
		ErrorText string `json:"errorText"`
	}
	if err := b.conn.call(ctx, b.session, "Page.navigate", map[string]any{"url": url}, &res); err != nil {
		return err
	}
	if res.ErrorText != "" {
		return fmt.Errorf("navigate: %w: %s", ErrNavigation, res.ErrorText)
	}
	return nil
}

// CurrentURL returns the URL the attached page is showing. It can carry
// tokens in its query, so callers must never print it.
func (b *Browser) CurrentURL(ctx context.Context) (string, error) {
	var res struct {
		TargetInfo struct {
			URL string `json:"url"`
		} `json:"targetInfo"`
	}
	if err := b.conn.call(ctx, "", "Target.getTargetInfo", map[string]any{"targetId": b.target}, &res); err != nil {
		return "", err
	}
	return res.TargetInfo.URL, nil
}

// Done is closed when the browser process exits.
func (b *Browser) Done() <-chan struct{} { return b.exited }

// Close asks the browser to quit, waits up to 10 s, then kills its process
// group. It also kills any helper left in the group after a clean exit, so no
// process from this launch outlives Close. It is safe to call more than once.
func (b *Browser) Close(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, closeTimeout)
	defer cancel()
	select {
	case <-b.exited:
	default:
		// The reply may never come; exit is what counts.
		_ = b.conn.call(ctx, "", "Browser.close", nil, nil)
		select {
		case <-b.exited:
		case <-ctx.Done():
			b.kill()
			return fmt.Errorf("close browser: %w; killed", ctx.Err())
		}
	}
	_ = b.killGroup()
	return nil
}

// kill kills the process group and waits for the browser to be reaped.
func (b *Browser) kill() {
	_ = b.killGroup()
	<-b.exited
}

// killGroup SIGKILLs the browser's process group, which holds its helpers,
// at most once per Browser.
func (b *Browser) killGroup() error {
	err := os.ErrProcessDone
	b.killOnce.Do(func() {
		err = syscall.Kill(-b.cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			err = os.ErrProcessDone
		}
	})
	return err
}

// FindChromium returns the first usable browser binary from, in order:
// $FBM_CHROMIUM, the install.sh Chrome for Testing paths, chromium,
// chromium-browser and google-chrome on PATH, and the macOS Chrome app.
// A set but unusable $FBM_CHROMIUM is an error, not a fallthrough.
func FindChromium() (string, error) {
	if p := os.Getenv("FBM_CHROMIUM"); p != "" {
		if !isExecutable(p) {
			return "", fmt.Errorf("FBM_CHROMIUM %s is not an executable file", p)
		}
		return p, nil
	}
	if home, err := os.UserHomeDir(); err == nil {
		for _, d := range []string{"chrome-linux64", "chrome-linux-arm64"} {
			if p := filepath.Join(home, ".local/share/fbm", d, "chrome"); isExecutable(p) {
				return p, nil
			}
		}
	}
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	if p := "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"; isExecutable(p) {
		return p, nil
	}
	return "", errors.New("no Chromium or Chrome binary found; set FBM_CHROMIUM")
}

func isExecutable(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}
