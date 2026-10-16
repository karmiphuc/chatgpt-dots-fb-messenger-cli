package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/state"
)

const (
	cookiesFile = "cookies.json"
	sessionFile = "session.json"
	keyFile     = "fbm.key"
	verdictFile = "spike-verdict.json"
)

type fileStatus struct {
	Present bool  `json:"present"`
	AgeS    int64 `json:"age_s"`
}

type sessionStatus struct {
	Present bool  `json:"present"`
	AgeS    int64 `json:"age_s"`
	Size    int64 `json:"size"`
}

type keyStatus struct {
	Present bool `json:"present"`
}

// statusObject is the `fbm status` output. It carries presence, ages and sizes
// only: never file contents, except the spike verdict, which holds no secrets.
type statusObject struct {
	Cookies     fileStatus      `json:"cookies"`
	Session     sessionStatus   `json:"session"`
	Key         keyStatus       `json:"key"`
	LastVerdict json.RawMessage `json:"last_verdict"`
}

func buildStatus(st *state.Store) statusObject {
	var s statusObject
	if _, age, ok := st.Stat(cookiesFile); ok {
		s.Cookies = fileStatus{Present: true, AgeS: int64(age / time.Second)}
	}
	if size, age, ok := st.Stat(sessionFile); ok {
		s.Session = sessionStatus{Present: true, AgeS: int64(age / time.Second), Size: size}
	}
	_, _, s.Key.Present = st.Stat(keyFile)
	if raw, err := st.ReadSecret(verdictFile); err == nil {
		var compact bytes.Buffer
		if json.Compact(&compact, raw) == nil {
			s.LastVerdict = compact.Bytes()
		}
	}
	return s
}

func parseStatusFlags(args []string, errOut io.Writer) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(errOut)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	return nil
}

func runStatus(args []string) int {
	if err := parseStatusFlags(args, errOut); err != nil {
		return 2
	}
	st, err := state.Open("")
	if err != nil {
		fmt.Fprintf(errOut, "fbm status: %v\n", err)
		return 1
	}
	if err := writeJSON(os.Stdout, buildStatus(st)); err != nil {
		fmt.Fprintf(errOut, "fbm status: %v\n", err)
		return 1
	}
	return 0
}
