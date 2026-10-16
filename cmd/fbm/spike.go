package main

import (
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

	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/meta"
	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/state"
)

const (
	spikeLogFile = "spike.log"
	// maxSpikeHours keeps the deadline timer far from time.Duration overflow.
	maxSpikeHours = 24 * 365
)

type spikeOpts struct {
	hours float64
	proxy string
}

func parseSpikeFlags(args []string, errOut io.Writer) (spikeOpts, error) {
	var o spikeOpts
	fs := flag.NewFlagSet("spike", flag.ContinueOnError)
	fs.SetOutput(errOut)
	fs.Float64Var(&o.hours, "hours", 48, "hours to keep the session idle-connected (fractions allowed)")
	fs.StringVar(&o.proxy, "proxy", "", "proxy URL for Facebook traffic")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	switch {
	case fs.NArg() > 0:
		return o, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	case !(o.hours > 0) || o.hours > maxSpikeHours:
		return o, fmt.Errorf("--hours must be > 0 and <= %d", maxSpikeHours)
	}
	return o, nil
}

// spikeExit is the process exit code for a verdict: 0 survived, 3 when Facebook
// checkpointed or logged the account out or the socket would not stay up, 1
// otherwise.
func spikeExit(outcome string) int {
	switch outcome {
	case meta.OutcomeSurvived:
		return 0
	case meta.OutcomeCheckpointed, meta.OutcomeLoggedOut, meta.OutcomeDisconnected:
		return 3
	}
	return 1
}

func runSpike(args []string) int {
	o, err := parseSpikeFlags(args, errOut)
	if err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintf(errOut, "fbm spike: %v\n", err)
		}
		return 2
	}
	st, err := state.Open("")
	if err != nil {
		fmt.Fprintf(errOut, "fbm spike: %v\n", err)
		return 1
	}
	// The hours deadline is RunSpike's own timer: Open's ctx must outlive the
	// connection, so it carries no timeout.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return spike(ctx, os.Stdout, st, o)
}

// spike runs the spike under the single-owner lock, after the checkpoint latch
// allows it. The lock is held for the whole run.
func spike(ctx context.Context, w io.Writer, st *state.Store, o spikeOpts) int {
	release, code := enterGate(w, st, "spike", false)
	if release == nil {
		return code
	}
	defer release()
	c, err := loadCookies(st)
	if err != nil {
		fmt.Fprintf(errOut, "fbm spike: %v\n", err)
		return 1
	}
	logf, err := os.OpenFile(filepath.Join(st.Dir, spikeLogFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintf(errOut, "fbm spike: %v\n", err)
		return 1
	}
	defer logf.Close()
	startedAt := time.Now()

	// One Open attempt (cached state when usable): a failure is the verdict,
	// never a retry, so a checkpoint is not retried into a ban.
	s, err := meta.Open(ctx, st, c, o.proxy, false, state.NewLogger(false))
	if err != nil {
		return finishSpike(w, st, meta.OpenFailedVerdict(startedAt, o.hours, err), true)
	}
	v, err := meta.RunSpike(ctx, s, st, o.hours, logf)
	closeSession(st, s, c, "spike")
	if err != nil {
		fmt.Fprintf(errOut, "fbm spike: verdict not saved: %v\n", err)
	}
	return finishSpike(w, st, v, false)
}

// finishSpike latches a checkpoint-class fatal_class, persists v when save is
// set (RunSpike saves its own), prints it, and returns the exit code. A save
// failure is reported but does not change the code: the verdict is still
// printed.
func finishSpike(w io.Writer, st *state.Store, v *meta.Verdict, save bool) int {
	recordCheckpoint(st, "spike", meta.Class(v.FatalClass))
	if save {
		if err := meta.WriteVerdict(st, v); err != nil {
			fmt.Fprintf(errOut, "fbm spike: verdict not saved: %v\n", err)
		}
	}
	if err := writeJSON(w, v); err != nil {
		return 1
	}
	return spikeExit(v.Outcome)
}
