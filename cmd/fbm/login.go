package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix/cookies"

	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/auth"
	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/meta"
	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/state"
)

type loginOpts struct {
	keygen         bool
	cookies        string
	browser        bool
	timeout        time.Duration
	ownerCleared   bool
	allowNoSandbox bool
	proxy          string
	keep           bool
}

func parseLoginFlags(args []string, errOut io.Writer) (loginOpts, error) {
	var o loginOpts
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(errOut)
	fs.BoolVar(&o.keygen, "keygen", false, "create the age key and print its public key")
	fs.StringVar(&o.cookies, "cookies", "", "cookie export to import (plain or age-encrypted)")
	fs.BoolVar(&o.browser, "browser", false, "log in through a Chromium window on this computer")
	fs.DurationVar(&o.timeout, "timeout", 15*time.Minute, "with --browser: how long to wait for the owner to log in")
	fs.BoolVar(&o.ownerCleared, "owner-cleared", false, "the owner cleared a Facebook checkpoint: drop the latch")
	fs.BoolVar(&o.allowNoSandbox, "allow-no-sandbox", false, "with --browser: run Chromium without its sandbox (owner approval only)")
	fs.StringVar(&o.proxy, "proxy", "", "proxy URL for Facebook traffic; with --browser Chromium uses it too, so no credentials")
	fs.BoolVar(&o.keep, "keep", false, "keep the cookie export after a successful login")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	modes := 0
	for _, m := range []bool{o.keygen, o.cookies != "", o.browser} {
		if m {
			modes++
		}
	}
	switch {
	case fs.NArg() > 0:
		return o, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	case modes > 1:
		return o, errors.New("--keygen, --cookies and --browser are mutually exclusive")
	case modes == 0:
		return o, errors.New("one of --keygen, --cookies or --browser is required")
	case o.keygen && o.ownerCleared:
		return o, errors.New("--owner-cleared needs --cookies or --browser")
	case !o.browser && (set["timeout"] || o.allowNoSandbox):
		return o, errors.New("--timeout and --allow-no-sandbox need --browser")
	case o.browser && o.keep:
		return o, errors.New("--keep needs --cookies")
	case o.timeout < time.Second:
		return o, errors.New("--timeout must be at least 1s")
	}
	return o, nil
}

type loginResult struct {
	UserIDLen             int    `json:"user_id_len"`
	Name                  string `json:"name"`
	ThreadsInInitialTable int    `json:"threads_in_initial_table"`
	Socket                string `json:"socket"`
}

func runLogin(args []string) int {
	o, err := parseLoginFlags(args, errOut)
	if err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintf(errOut, "fbm login: %v\n", err)
		}
		return 2
	}
	st, err := state.Open("")
	if err != nil {
		fmt.Fprintf(errOut, "fbm login: %v\n", err)
		return 1
	}
	if o.keygen {
		pub, err := auth.Keygen(st.Dir)
		if err != nil {
			fmt.Fprintf(errOut, "fbm login: %v\n", err)
			return 1
		}
		_ = writeJSON(os.Stdout, map[string]string{"public_key": pub})
		return 0
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return login(ctx, os.Stdout, st, o)
}

// login runs --cookies or --browser under the single-owner lock, after the
// checkpoint latch allows it.
func login(ctx context.Context, w io.Writer, st *state.Store, o loginOpts) int {
	release, code := enterGate(w, st, "login", o.ownerCleared)
	if release == nil {
		return code
	}
	defer release()
	if o.browser {
		return loginBrowser(ctx, w, st, o)
	}
	return loginCookies(ctx, w, st, o)
}

// loginCookies imports the cookie export at o.cookies.
func loginCookies(ctx context.Context, w io.Writer, st *state.Store, o loginOpts) int {
	// Plaintext stays in memory: no temp file is ever created for it.
	raw, err := auth.DecryptIfArmored(o.cookies, filepath.Join(st.Dir, keyFile))
	if err != nil {
		fmt.Fprintf(errOut, "fbm login: %v\n", err)
		return 1
	}
	c, err := auth.ParseCookieExport(bytes.NewReader(raw))
	if err != nil {
		fmt.Fprintf(errOut, "fbm login: %v\n", err)
		return 1
	}
	return connectLogin(ctx, w, st, c, o)
}

// connectLogin drops session.json, opens a fresh session with c and, only when
// the socket connects, stores c. A checkpoint-class failure is latched.
func connectLogin(ctx context.Context, w io.Writer, st *state.Store, c *cookies.Cookies, o loginOpts) int {
	s, err := openLogin(ctx, st, c, o.proxy)
	if err != nil {
		var ce *meta.ClassifiedError
		if !errors.As(err, &ce) {
			fmt.Fprintf(errOut, "fbm login: %v\n", err)
			return 1
		}
		recordCheckpoint(st, "login", ce.Class)
		return reportError(w, ce)
	}
	socket, perr := waitSocket(ctx, s, socketWait)
	return finishLogin(w, st, s, c, o, socket, perr)
}

// openLogin removes any session.json from an earlier login, then opens fresh.
// Close never overwrites the file with an empty dump, so a re-login that fails
// before a sync would otherwise leave the old state for the spike to load. A
// local removal failure is a plain error; Open's errors are *ClassifiedError.
func openLogin(ctx context.Context, st *state.Store, c *cookies.Cookies, proxy string) (*meta.Session, error) {
	if err := st.Remove(sessionFile); err != nil {
		return nil, fmt.Errorf("drop old session state: %w", err)
	}
	return meta.Open(ctx, st, c, proxy, true, state.NewLogger(false))
}

// finishLogin closes s and, only when the socket connected, stores the cookies,
// deletes the export (unless --keep, or there is none) and prints the result.
// Any other socket result prints its error_class, latching a checkpoint class,
// and keeps the export for another try.
func finishLogin(w io.Writer, st *state.Store, s *meta.Session, c *cookies.Cookies, o loginOpts,
	socket string, perr *meta.ClassifiedError) int {
	// Close persists session state, or drops it after a permanent error.
	defer s.Close(st)
	if socket != "connected" {
		if perr != nil {
			recordCheckpoint(st, "login", perr.Class)
		}
		return reportSocket(w, "login", socket, perr)
	}

	if err := writeCookies(st, c); err != nil {
		fmt.Fprintf(errOut, "fbm login: %v\n", err)
		return 1
	}
	if o.cookies != "" && !o.keep {
		if err := os.Remove(o.cookies); err != nil {
			fmt.Fprintf(errOut, "fbm login: warning: could not delete %s: %v\n", o.cookies, err)
		}
	}

	res := loginResult{UserIDLen: len(c.Get(cookies.FBCookieCUser)), Socket: socket}
	if s.Me != nil {
		res.Name = s.Me.GetName()
	}
	if s.Initial != nil {
		res.ThreadsInInitialTable = len(s.Initial.LSDeleteThenInsertThread)
	}
	if err := writeJSON(w, res); err != nil {
		return 1
	}
	return 0
}

func writeCookies(st *state.Store, c *cookies.Cookies) error {
	data, err := c.MarshalJSON()
	if err != nil {
		return fmt.Errorf("encode cookies: %w", err)
	}
	return st.WriteSecret(cookiesFile, data)
}
