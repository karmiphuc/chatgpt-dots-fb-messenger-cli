package main

import (
	"fmt"
	"io"
	"os"

	"github.com/rs/zerolog"
	zlog "github.com/rs/zerolog/log"
)

// init silences zerolog's global logger before any command runs. messagix's
// lightspeed decoder and table code log through it, not through the logger fbm
// hands the client, and at its default trace level it prints raw
// stored-procedure arguments (sender names, message text) to stderr.
func init() {
	zlog.Logger = zerolog.Nop()
}

// errOut receives every diagnostic a command writes for humans; stdout carries
// only JSON. Tests point it at a buffer so expected-path messages stay out of
// the test output.
var errOut io.Writer = os.Stderr

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	switch args[0] {
	case "login":
		return runLogin(args[1:])
	case "threads":
		return runThreads(args[1:])
	case "status":
		return runStatus(args[1:])
	case "spike":
		return runSpike(args[1:])
	case "doctor":
		return runDoctor(args[1:])
	case "-h", "--help", "help":
		usage()
		return 0
	default:
		fmt.Fprintf(errOut, "fbm: unknown subcommand %q\n", args[0])
		usage()
		return 2
	}
}

func usage() {
	fmt.Fprintln(errOut, `usage: fbm <subcommand> [args]

subcommands:
  login     log in (--browser, or --cookies import) and store session state
  threads   list Messenger threads
  status    show session state (lengths and ages only)
  spike     run the M0 protocol spike
  doctor    report what this VM offers fbm (--env [--launch-check])`)
}
