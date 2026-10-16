package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/browser"
	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/state"
)

// launchAttemptTimeout bounds each launch attempt on its own, so a sandboxed
// attach that hangs cannot starve the no-sandbox diagnosis. A var so tests can
// shorten it.
var launchAttemptTimeout = 40 * time.Second

// Seams tests swap to observe a launch without a real browser.
var (
	launchAttempt = tryLaunch
	browserLaunch = browser.Launch
)

type doctorOpts struct {
	env         bool
	launchCheck bool
}

type chromiumInfo struct {
	Path    string  `json:"path"`
	Version *string `json:"version"`
}

// doctorReport is the `fbm doctor --env` output. It describes the VM and the
// state dir's shape (presence, a checkpoint class), never file contents, and
// is built without contacting Meta. The launch fields are pointers so they are
// absent, not false, unless --launch-check ran.
type doctorReport struct {
	Arch        string        `json:"arch"`
	GoVersion   string        `json:"go_version"`
	Chromium    *chromiumInfo `json:"chromium"`
	Display     bool          `json:"display"`
	UID         int           `json:"uid"`
	Sudo        string        `json:"sudo"`
	SystemdUser bool          `json:"systemd_user"`
	ShmMB       *int64        `json:"shm_mb"`
	StateDir    string        `json:"state_dir"`
	LoggedIn    bool          `json:"logged_in"`
	LastFatal   *string       `json:"last_fatal"`
	Isolation   string        `json:"isolation"`

	LaunchOK       *bool `json:"launch_ok,omitempty"`
	SandboxOK      *bool `json:"sandbox_ok,omitempty"`
	NeedsNoSandbox *bool `json:"needs_no_sandbox,omitempty"`
}

// launchResult is what the launch check learned. OK means Chromium launched and
// answered CDP in any mode; SandboxOK means the sandboxed launch did;
// NeedsNoSandbox means only the --no-sandbox launch did.
type launchResult struct {
	OK             bool
	SandboxOK      bool
	NeedsNoSandbox bool
}

func parseDoctorFlags(args []string, errOut io.Writer) (doctorOpts, error) {
	var o doctorOpts
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(errOut)
	fs.BoolVar(&o.env, "env", false, "report what this VM offers fbm")
	fs.BoolVar(&o.launchCheck, "launch-check", false, "also launch Chromium once at about:blank (needs --env)")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if fs.NArg() > 0 {
		return o, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if !o.env {
		return o, fmt.Errorf("doctor needs --env")
	}
	return o, nil
}

func runDoctor(args []string) int {
	// A signal cancels the context so a launch in flight is killed and its
	// throwaway profile removed, as in the commands that run a browser.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return doctor(ctx, os.Stdout, args)
}

// doctor takes neither the lock nor the latch: it never contacts Meta. A
// failing check is a field in the JSON, so the exit code is 0; only bad flags
// or an unusable state dir fail.
func doctor(ctx context.Context, w io.Writer, args []string) int {
	o, err := parseDoctorFlags(args, errOut)
	if err != nil {
		fmt.Fprintf(errOut, "fbm doctor: %v\n", err)
		return 2
	}
	st, err := state.Open("")
	if err != nil {
		fmt.Fprintf(errOut, "fbm doctor: %v\n", err)
		return 1
	}
	rep := buildDoctor(st)
	if o.launchCheck {
		applyLaunchCheck(ctx, &rep)
		if ctx.Err() != nil {
			fmt.Fprintln(errOut, "fbm doctor: interrupted")
			return 1
		}
	}
	if err := writeJSON(w, rep); err != nil {
		fmt.Fprintf(errOut, "fbm doctor: %v\n", err)
		return 1
	}
	return 0
}

func buildDoctor(st *state.Store) doctorReport {
	rep := doctorReport{
		Arch:        runtime.GOARCH,
		GoVersion:   runtime.Version(),
		Chromium:    chromiumFacts(),
		Display:     hasDisplay(),
		UID:         os.Getuid(),
		Sudo:        sudoMode(),
		SystemdUser: systemdUser(os.Getuid()),
		ShmMB:       shmMB(),
		StateDir:    st.Dir,
		Isolation:   "advisory",
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.GoVersion != "" {
		rep.GoVersion = bi.GoVersion
	}
	_, _, rep.LoggedIn = st.Stat(cookiesFile)
	if class, _, ok := st.LastFatal(); ok {
		class = reportableFatal(class)
		rep.LastFatal = &class
	}
	return rep
}

// reportableFatal limits last_fatal to the classes fbm itself records. The
// file is owner-writable text, so anything else prints as "unreadable" rather
// than reaching the JSON a Dot pastes back.
func reportableFatal(class string) string {
	switch class {
	case "checkpoint", "challenge", "suspended", "rate_limited", state.UnreadableFatal:
		return class
	}
	return state.UnreadableFatal
}

// chromiumFacts is the browser fbm would launch and its --version line, or nil
// when there is none. A binary that will not report a version keeps its path.
func chromiumFacts() *chromiumInfo {
	bin, err := browser.FindChromium()
	if err != nil {
		return nil
	}
	info := &chromiumInfo{Path: bin}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--version")
	cmd.WaitDelay = time.Second // a forked child holding the pipe cannot outlast the timeout
	if out, err := cmd.Output(); err == nil {
		if v := strings.TrimSpace(string(out)); v != "" {
			info.Version = &v
		}
	}
	return info
}

// sudoMode is "nopasswd" when `sudo -n true` succeeds, else "none".
func sudoMode() string {
	path, err := exec.LookPath("sudo")
	if err != nil {
		return "none"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "-n", "true")
	cmd.WaitDelay = time.Second
	if cmd.Run() != nil {
		return "none"
	}
	return "nopasswd"
}

// systemdUser reports whether uid has a systemd user manager listening.
func systemdUser(uid int) bool {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = filepath.Join("/run/user", strconv.Itoa(uid))
	}
	fi, err := os.Stat(filepath.Join(dir, "systemd", "private"))
	return err == nil && fi.Mode()&os.ModeSocket != 0
}

// shmMB is the size of /dev/shm in MiB on Linux, where Chromium needs it, and
// nil elsewhere or when it cannot be read.
func shmMB() *int64 {
	if runtime.GOOS != "linux" {
		return nil
	}
	var s syscall.Statfs_t
	if syscall.Statfs("/dev/shm", &s) != nil {
		return nil
	}
	mb := int64(uint64(s.Bsize) * uint64(s.Blocks) / (1 << 20))
	return &mb
}

func applyLaunchCheck(ctx context.Context, rep *doctorReport) {
	var res launchResult
	if rep.Chromium == nil {
		fmt.Fprintln(errOut, "fbm doctor: launch check skipped: no Chromium or Chrome binary found")
	} else {
		res = diagnoseLaunch(func(noSandbox bool) error {
			actx, cancel := context.WithTimeout(ctx, launchAttemptTimeout)
			defer cancel()
			return launchAttempt(actx, rep.Chromium.Path, noSandbox, !rep.Display)
		})
	}
	rep.LaunchOK, rep.SandboxOK, rep.NeedsNoSandbox = &res.OK, &res.SandboxOK, &res.NeedsNoSandbox
}

// diagnoseLaunch runs attempt sandboxed first. When that fails it tries once
// with the sandbox off, only to learn whether the sandbox is the cause: the
// no-sandbox browser is never used for anything else. A sandboxed success ends
// the check, since a second attempt could not change the result, even as root.
func diagnoseLaunch(attempt func(noSandbox bool) error) launchResult {
	if attempt(false) == nil {
		return launchResult{OK: true, SandboxOK: true}
	}
	ok := attempt(true) == nil
	return launchResult{OK: ok, NeedsNoSandbox: ok}
}

// tryLaunch starts bin on a throwaway profile at about:blank, makes one CDP
// round trip and closes it. It never navigates and never prints the URL.
func tryLaunch(ctx context.Context, bin string, noSandbox, headless bool) error {
	tmp, err := os.MkdirTemp("", "fbm-doctor-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	profile, err := filepath.Abs(tmp)
	if err != nil {
		return err
	}
	lctx, cancel := context.WithCancel(ctx)
	defer cancel()
	b, err := browserLaunch(lctx, browser.LaunchOptions{
		Binary: bin, ProfileDir: profile, AllowNoSandbox: noSandbox, Headless: headless,
	})
	if err != nil {
		return err
	}
	defer b.Close(ctx)
	_, err = b.CurrentURL(ctx)
	return err
}
