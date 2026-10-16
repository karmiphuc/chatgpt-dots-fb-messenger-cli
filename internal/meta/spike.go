package meta

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/state"
)

// VerdictFile is the spike verdict's name in the state dir. `fbm status` echoes
// it, so it holds counts, timestamps and class strings only.
const VerdictFile = "spike-verdict.json"

// Spike outcomes.
const (
	OutcomeSurvived     = "survived"
	OutcomeCheckpointed = "checkpointed"
	OutcomeLoggedOut    = "logged_out"
	OutcomeAborted      = "aborted"
	// OutcomeDisconnected: the socket never held, or was down when the run
	// ended, or kept failing. Upstream reports most post-checkpoint failures
	// (including an HTTP 401/403 on the websocket upgrade) as transient
	// disconnects it retries forever, so this is a failure verdict, not a pass.
	OutcomeDisconnected = "disconnected"
)

// MaxConsecutiveTransient is the circuit breaker: this many transient
// disconnects with no connected/reconnected between them end the run (or the
// CLI's socket wait), so messagix stops retrying against a possibly locked
// account.
const MaxConsecutiveTransient = 3

// Verdict is the spike result. LongestGap is the longest span from a
// transient_disconnect to the next connected or reconnected event; a span still
// open at the end counts up to EndedAt. FatalClass is the error class that ended
// the run; for disconnected it is the class of the last transient disconnect,
// empty when none did.
type Verdict struct {
	StartedAt            time.Time
	EndedAt              time.Time
	PlannedHours         float64
	Outcome              string
	FatalClass           string
	Connected            int
	Reconnected          int
	TransientDisconnects int
	Tables               int
	LongestGap           time.Duration
}

// MarshalJSON emits the snake_case verdict file shape, with the gap in float
// seconds. Nothing but counts, timestamps and class strings is representable.
func (v Verdict) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		StartedAt            time.Time `json:"started_at"`
		EndedAt              time.Time `json:"ended_at"`
		PlannedHours         float64   `json:"planned_hours"`
		Outcome              string    `json:"outcome"`
		FatalClass           string    `json:"fatal_class"`
		Connected            int       `json:"connected"`
		Reconnected          int       `json:"reconnected"`
		TransientDisconnects int       `json:"transient_disconnects"`
		Tables               int       `json:"tables"`
		LongestGapS          float64   `json:"longest_gap_s"`
	}{v.StartedAt, v.EndedAt, v.PlannedHours, v.Outcome, v.FatalClass,
		v.Connected, v.Reconnected, v.TransientDisconnects, v.Tables, v.LongestGap.Seconds()})
}

// WriteVerdict stores v as spike-verdict.json. It holds no secret, but the
// state dir policy is uniform, so it goes through WriteSecret.
func WriteVerdict(st *state.Store, v *Verdict) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return st.WriteSecret(VerdictFile, raw)
}

// outcomeFor maps the class of a run-ending error. Any permanent error ends the
// run: messagix stops reconnecting after one.
func outcomeFor(c Class) string {
	switch c {
	case ClassNotLoggedIn:
		return OutcomeLoggedOut
	case ClassCheckpoint, ClassChallenge, ClassSuspended, ClassRateLimited:
		return OutcomeCheckpointed
	}
	return OutcomeAborted
}

// OpenFailedVerdict is the verdict for a run whose Open failed before any event:
// zero counts, started_at == ended_at.
func OpenFailedVerdict(at time.Time, hours float64, err error) *Verdict {
	c := Classify(err).Class
	return &Verdict{StartedAt: at, EndedAt: at, PlannedHours: hours, Outcome: outcomeFor(c), FatalClass: string(c)}
}

// spikeLine is one spike.log line: the event kind and, when the event carried
// an error, its class. Never the error text.
type spikeLine struct {
	At     time.Time `json:"at"`
	Kind   string    `json:"kind"`
	Reason string    `json:"reason"`
}

// RunSpike consumes s.Events until the hours deadline, a fatal event, the
// transient-disconnect breaker, or ctx is done, and writes the verdict on every
// exit path. A permanent_error, or a transient_disconnect of a fatal class,
// ends the run at once whatever else is queued; it never waits for a retry.
// At the deadline the run is "survived" only if the socket connected at least
// once and is up. A fatal transient or the breaker marks s permanent so Close
// drops the cached state. It reads s.Events only; s.Client is the caller's to
// close.
func RunSpike(ctx context.Context, s *Session, st *state.Store, hours float64, logw io.Writer) (*Verdict, error) {
	return runSpike(ctx, s, st, hours, logw, time.Now)
}

// runSpike is RunSpike with the clock injectable. now stamps the verdict and
// closes a still-open gap; event times (evt.At) measure the gaps in between.
func runSpike(ctx context.Context, s *Session, st *state.Store, hours float64, logw io.Writer, now func() time.Time) (*Verdict, error) {
	if hours <= 0 {
		return nil, errors.New("hours must be > 0")
	}
	v := &Verdict{StartedAt: now(), PlannedHours: hours}
	deadline := time.NewTimer(time.Duration(hours * float64(time.Hour)))
	defer deadline.Stop()
	enc := json.NewEncoder(logw)

	var (
		gapStart  time.Time
		gapOpen   bool
		consec    int
		lastClass Class // class of the last transient_disconnect
	)
	closeGap := func(end time.Time) {
		if !gapOpen {
			return
		}
		gapOpen = false
		if d := end.Sub(gapStart); d > v.LongestGap {
			v.LongestGap = d
		}
	}
	end := func(outcome string, class Class) {
		v.Outcome, v.FatalClass = outcome, string(class)
	}
	// handle logs and counts one event; it reports whether the run is over
	// (v.Outcome set).
	handle := func(evt Event) bool {
		var class Class
		if evt.Err != nil {
			class = Classify(evt.Err).Class
		}
		// A failed log write is not worth stopping the spike for.
		_ = enc.Encode(spikeLine{At: evt.At, Kind: evt.Kind, Reason: string(class)})

		switch evt.Kind {
		case KindConnected:
			v.Connected++
			consec = 0
			closeGap(evt.At)
		case KindReconnected:
			v.Reconnected++
			consec = 0
			closeGap(evt.At)
		case KindTransientDisconnect:
			v.TransientDisconnects++
			consec++
			lastClass = class
			if !gapOpen {
				gapOpen, gapStart = true, evt.At
			}
			// Either way the cached state is suspect: mark the session so
			// Close drops it, as after a permanent_error.
			switch {
			case IsFatal(class):
				s.MarkPermanent()
				end(outcomeFor(class), class)
				return true
			case consec >= MaxConsecutiveTransient:
				s.MarkPermanent()
				end(OutcomeDisconnected, lastClass)
				return true
			}
		case KindTable:
			v.Tables++
		case KindPermanentError:
			if class == "" {
				class = ClassUnknown
			}
			end(outcomeFor(class), class)
			return true
		}
		return false
	}
	// drain handles what is already queued, without blocking. select picks
	// randomly among ready cases, so the deadline or ctx can win over a queued
	// fatal event; draining stops that from losing its class. It reports
	// whether a queued event ended the run.
	drain := func() bool {
		for {
			select {
			case evt, ok := <-s.Events:
				if !ok {
					return false
				}
				if handle(evt) {
					return true
				}
			default:
				return false
			}
		}
	}
	// stopped finalises a deadline or ctx exit: a queued fatal event wins, then
	// a permanent error whose event was never read (class unknown), then the
	// caller's outcome.
	stopped := func(outcome func()) {
		if drain() {
			return
		}
		if s.permanent.Load() {
			end(OutcomeAborted, ClassUnknown)
			return
		}
		outcome()
	}

loop:
	for {
		select {
		case <-deadline.C:
			stopped(func() {
				if v.Connected+v.Reconnected > 0 && !gapOpen {
					end(OutcomeSurvived, "")
				} else {
					end(OutcomeDisconnected, lastClass)
				}
			})
			break loop
		case <-ctx.Done():
			stopped(func() { end(OutcomeAborted, "") })
			break loop
		case evt, ok := <-s.Events:
			if !ok {
				end(OutcomeAborted, "")
				break loop
			}
			if handle(evt) {
				break loop
			}
		}
	}

	v.EndedAt = now()
	closeGap(v.EndedAt)
	return v, WriteVerdict(st, v)
}
