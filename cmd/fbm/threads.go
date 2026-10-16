package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"go.mau.fi/mautrix-meta/pkg/messagix/cookies"
	"go.mau.fi/mautrix-meta/pkg/messagix/types"

	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/meta"
	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/state"
)

type threadsOpts struct {
	limit  int
	format string
	proxy  string
}

func parseThreadsFlags(args []string, errOut io.Writer) (threadsOpts, error) {
	var o threadsOpts
	fs := flag.NewFlagSet("threads", flag.ContinueOnError)
	fs.SetOutput(errOut)
	fs.IntVar(&o.limit, "limit", 50, "maximum number of threads (0 = all)")
	fs.StringVar(&o.format, "format", "json", "output format: json or text")
	fs.StringVar(&o.proxy, "proxy", "", "proxy URL for Facebook traffic")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	switch {
	case fs.NArg() > 0:
		return o, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	case o.limit < 0:
		return o, errors.New("--limit must be >= 0")
	case o.format != "json" && o.format != "text":
		return o, fmt.Errorf("--format must be json or text, got %q", o.format)
	}
	return o, nil
}

// loadCookies reads cookies.json from the store. Error text never includes
// file contents.
func loadCookies(st *state.Store) (*cookies.Cookies, error) {
	raw, err := st.ReadSecret(cookiesFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errors.New("no cookies.json: run `fbm login` first")
		}
		return nil, err
	}
	c := &cookies.Cookies{}
	if err := c.UnmarshalJSON(raw); err != nil {
		return nil, errors.New("cookies.json is not valid: run `fbm login` again")
	}
	c.Platform = types.Facebook
	if len(c.GetMissingCookieNames()) > 0 {
		return nil, errors.New("cookies.json is incomplete: run `fbm login` again")
	}
	return c, nil
}

func runThreads(args []string) int {
	o, err := parseThreadsFlags(args, errOut)
	if err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintf(errOut, "fbm threads: %v\n", err)
		}
		return 2
	}
	st, err := state.Open("")
	if err != nil {
		fmt.Fprintf(errOut, "fbm threads: %v\n", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return threads(ctx, os.Stdout, st, o)
}

// threads lists threads under the single-owner lock, after the checkpoint
// latch allows it.
func threads(ctx context.Context, w io.Writer, st *state.Store, o threadsOpts) int {
	release, code := enterGate(w, st, "threads", false)
	if release == nil {
		return code
	}
	defer release()
	c, err := loadCookies(st)
	if err != nil {
		fmt.Fprintf(errOut, "fbm threads: %v\n", err)
		return 1
	}

	s, err := meta.Open(ctx, st, c, o.proxy, true, state.NewLogger(false))
	if err != nil {
		ce := meta.Classify(err)
		recordCheckpoint(st, "threads", ce.Class)
		return reportError(w, ce)
	}
	socket, perr := waitSocket(ctx, s, socketWait)
	defer closeSession(st, s, c, "threads")
	return finishThreads(w, st, s, o, socket, perr)
}

// finishThreads prints one row per thread only when the socket connected. Any
// other socket result prints its error_class, latching a checkpoint class, and
// no rows.
func finishThreads(w io.Writer, st *state.Store, s *meta.Session, o threadsOpts, socket string, perr *meta.ClassifiedError) int {
	if socket != "connected" {
		if perr != nil {
			recordCheckpoint(st, "threads", perr.Class)
		}
		return reportSocket(w, "threads", socket, perr)
	}
	out := bufio.NewWriter(w)
	defer out.Flush()
	for _, r := range threadRows(s.Initial, o.limit) {
		if o.format == "text" {
			fmt.Fprintln(out, formatTextRow(r))
		} else if err := writeJSON(out, r); err != nil {
			return 1
		}
	}
	return 0
}
