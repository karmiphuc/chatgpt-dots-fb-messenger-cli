package meta

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.mau.fi/mautrix-meta/pkg/messagix"
	"go.mau.fi/mautrix-meta/pkg/messagix/cookies"
	"go.mau.fi/mautrix-meta/pkg/messagix/httpclient"
	"go.mau.fi/mautrix-meta/pkg/messagix/types"

	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/state"
)

// secretText stands in for upstream error text that can embed redirect URLs.
const secretText = "https://facebook.com/checkpoint/?next=SECRET-REDIRECT"

// fakeClock is a manually advanced clock for runSpike.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
	return c.t
}

func spikeFixture(t *testing.T) (*state.Store, *Session) {
	t.Helper()
	st, err := state.Open(filepath.Join(t.TempDir(), "state"))
	require.NoError(t, err)
	// Only Events: RunSpike must never touch s.Client.
	return st, &Session{Events: make(chan Event, eventBuffer)}
}

func readVerdict(t *testing.T, st *state.Store) map[string]any {
	t.Helper()
	raw, err := st.ReadSecret("spike-verdict.json")
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	return m
}

func TestRunSpikeStopsOnCheckpoint(t *testing.T) {
	st, s := spikeFixture(t)
	// A reconnect between the drops keeps the 3-in-a-row breaker from firing
	// before the permanent_error.
	for _, kind := range []string{KindTransientDisconnect, KindTransientDisconnect, KindReconnected, KindTransientDisconnect} {
		s.Events <- Event{At: time.Now(), Kind: kind}
	}
	s.Events <- Event{At: time.Now(), Kind: KindPermanentError,
		Err: Classify(fmt.Errorf("%s: %w", secretText, httpclient.ErrCheckpointRequired))}

	var logw bytes.Buffer
	begin := time.Now()
	v, err := RunSpike(context.Background(), s, st, 1, &logw)

	require.NoError(t, err)
	require.Less(t, time.Since(begin), 5*time.Second, "must return before the 1h deadline")
	require.Equal(t, "checkpointed", v.Outcome)
	require.Equal(t, "checkpoint", v.FatalClass)
	require.Equal(t, 3, v.TransientDisconnects)
	require.Contains(t, logw.String(), `"reason":"checkpoint"`)
	require.NotContains(t, logw.String(), "SECRET-REDIRECT")
	require.NotContains(t, logw.String(), "checkpoint/")

	m := readVerdict(t, st)
	require.Equal(t, "checkpointed", m["outcome"])
}

func TestRunSpikeSurvivesDeadline(t *testing.T) {
	st, s := spikeFixture(t)
	s.Events <- Event{At: time.Now(), Kind: KindConnected}
	s.Events <- Event{At: time.Now(), Kind: KindTable}
	s.Events <- Event{At: time.Now(), Kind: KindTable}

	var logw bytes.Buffer
	v, err := RunSpike(context.Background(), s, st, 0.00002, &logw) // 72ms
	require.NoError(t, err)
	require.Equal(t, "survived", v.Outcome)
	require.Empty(t, v.FatalClass)
	require.Equal(t, 1, v.Connected)
	require.Equal(t, 2, v.Tables)
	require.Equal(t, 0.00002, v.PlannedHours)
	require.Equal(t, "survived", readVerdict(t, st)["outcome"])
	require.Equal(t, 3, strings.Count(logw.String(), "\n"))
}

func TestRunSpikeOutcomeByClass(t *testing.T) {
	cases := map[string]struct {
		err     error
		outcome string
		class   string
	}{
		"not_logged_in": {httpclient.ErrTokenInvalidated, "logged_out", "not_logged_in"},
		"challenge":     {httpclient.ErrChallengeRequired, "checkpointed", "challenge"},
		"suspended":     {httpclient.ErrAccountSuspended, "checkpointed", "suspended"},
		"rate_limited":  {httpclient.ErrRateLimited, "checkpointed", "rate_limited"},
		"unknown":       {errors.New(secretText), "aborted", "unknown"},
		"nil error":     {nil, "aborted", "unknown"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			st, s := spikeFixture(t)
			evt := Event{At: time.Now(), Kind: KindPermanentError}
			if tc.err != nil {
				evt.Err = Classify(tc.err)
			}
			s.Events <- evt
			var logw bytes.Buffer
			v, err := RunSpike(context.Background(), s, st, 1, &logw)
			require.NoError(t, err)
			require.Equal(t, tc.outcome, v.Outcome)
			require.Equal(t, tc.class, v.FatalClass)
			require.NotContains(t, logw.String(), "SECRET-REDIRECT")
		})
	}
}

func TestRunSpikeContextCancelAborts(t *testing.T) {
	st, s := spikeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	v, err := RunSpike(ctx, s, st, 1, &bytes.Buffer{})
	require.NoError(t, err)
	require.Equal(t, "aborted", v.Outcome)
	require.Empty(t, v.FatalClass)
	require.Equal(t, "aborted", readVerdict(t, st)["outcome"])
}

func TestRunSpikeLongestGapAcrossPairs(t *testing.T) {
	st, s := spikeFixture(t)
	clk := &fakeClock{t: time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)}
	send := func(kind string, after time.Duration) {
		s.Events <- Event{At: clk.advance(after), Kind: kind}
	}
	send(KindConnected, 0)
	send(KindTransientDisconnect, time.Minute)
	send(KindReconnected, 30*time.Second) // gap 30s
	send(KindTransientDisconnect, time.Minute)
	send(KindTransientDisconnect, 2*time.Minute) // second drop while open: gap keeps its start
	send(KindReconnected, 2*time.Minute)         // gap 4m
	send(KindTransientDisconnect, time.Minute)
	send(KindConnected, 10*time.Second) // gap 10s

	v, err := runSpike(context.Background(), s, st, 0.00002, &bytes.Buffer{}, clk.now)
	require.NoError(t, err)
	require.Equal(t, "survived", v.Outcome)
	require.Equal(t, 4, v.TransientDisconnects)
	require.Equal(t, 4*time.Minute, v.LongestGap)
	require.Equal(t, 240.0, readVerdict(t, st)["longest_gap_s"])
}

func TestRunSpikeOpenGapCountsToEnd(t *testing.T) {
	st, s := spikeFixture(t)
	clk := &fakeClock{t: time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)}
	s.Events <- Event{At: clk.advance(0), Kind: KindTransientDisconnect}
	// The deadline timer is real; the verdict end time is the fake clock.
	go func() {
		time.Sleep(20 * time.Millisecond)
		clk.advance(90 * time.Second)
	}()
	v, err := runSpike(context.Background(), s, st, 0.00003, &bytes.Buffer{}, clk.now) // 108ms
	require.NoError(t, err)
	require.Equal(t, 90*time.Second, v.LongestGap)
	require.Equal(t, 90*time.Second, v.EndedAt.Sub(v.StartedAt))
}

func TestVerdictJSONShapeAndSecrecy(t *testing.T) {
	st, s := spikeFixture(t)
	s.Events <- Event{At: time.Now(), Kind: KindTransientDisconnect,
		Err: Classify(errors.New(secretText))}
	s.Events <- Event{At: time.Now(), Kind: KindPermanentError,
		Err: Classify(fmt.Errorf("%s: %w", secretText, httpclient.ErrCheckpointRequired))}
	_, err := RunSpike(context.Background(), s, st, 1, &bytes.Buffer{})
	require.NoError(t, err)

	raw, err := st.ReadSecret("spike-verdict.json")
	require.NoError(t, err)
	require.NotContains(t, string(raw), "SECRET-REDIRECT")
	require.NotContains(t, string(raw), "facebook.com")

	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	want := []string{"connected", "ended_at", "fatal_class", "longest_gap_s", "outcome", "planned_hours",
		"reconnected", "started_at", "tables", "transient_disconnects"}
	require.Equal(t, want, keys)
}

func TestOpenFailedVerdict(t *testing.T) {
	st, _ := spikeFixture(t)
	at := time.Date(2026, 10, 16, 1, 2, 3, 0, time.UTC)

	v := OpenFailedVerdict(at, 48, Classify(httpclient.ErrTokenInvalidated))
	require.Equal(t, "logged_out", v.Outcome)
	require.Equal(t, "not_logged_in", v.FatalClass)
	require.Equal(t, v.StartedAt, v.EndedAt)
	require.Zero(t, v.Connected+v.Reconnected+v.TransientDisconnects+v.Tables)

	v = OpenFailedVerdict(at, 48, Classify(errors.New(secretText)))
	require.Equal(t, "aborted", v.Outcome)
	require.Equal(t, "unknown", v.FatalClass)

	require.NoError(t, WriteVerdict(st, v))
	require.Equal(t, "aborted", readVerdict(t, st)["outcome"])
}

func TestRunSpikeRejectsNonPositiveHours(t *testing.T) {
	st, s := spikeFixture(t)
	_, err := RunSpike(context.Background(), s, st, 0, &bytes.Buffer{})
	require.Error(t, err)
}

func transient(err error) Event {
	e := Event{At: time.Now(), Kind: KindTransientDisconnect}
	if err != nil {
		e.Err = Classify(err)
	}
	return e
}

func TestRunSpikeDeadlineWithOpenGapIsDisconnected(t *testing.T) {
	st, s := spikeFixture(t)
	s.Events <- Event{At: time.Now(), Kind: KindConnected}
	s.Events <- transient(errors.New(secretText))
	v, err := RunSpike(context.Background(), s, st, 0.00002, &bytes.Buffer{})
	require.NoError(t, err)
	require.Equal(t, "disconnected", v.Outcome)
	require.Equal(t, "unknown", v.FatalClass)
	require.Equal(t, "disconnected", readVerdict(t, st)["outcome"])
}

func TestRunSpikeDeadlineWithZeroConnectsIsDisconnected(t *testing.T) {
	st, s := spikeFixture(t)
	s.Events <- Event{At: time.Now(), Kind: KindTable}
	v, err := RunSpike(context.Background(), s, st, 0.00002, &bytes.Buffer{})
	require.NoError(t, err)
	require.Equal(t, "disconnected", v.Outcome)
	require.Empty(t, v.FatalClass, "no transient seen, so no class")
}

func TestRunSpikeThreeConsecutiveTransientsBreak(t *testing.T) {
	st, s := spikeFixture(t)
	for i := 0; i < 3; i++ {
		s.Events <- transient(errors.New(secretText))
	}
	s.Events <- Event{At: time.Now(), Kind: KindReconnected} // never reached
	begin := time.Now()
	var logw bytes.Buffer
	v, err := RunSpike(context.Background(), s, st, 1, &logw)
	require.NoError(t, err)
	require.Less(t, time.Since(begin), 5*time.Second)
	require.Equal(t, "disconnected", v.Outcome)
	require.Equal(t, "unknown", v.FatalClass)
	require.Equal(t, 3, v.TransientDisconnects)
	require.Zero(t, v.Reconnected)
	require.NotContains(t, logw.String(), "SECRET-REDIRECT")
}

func TestRunSpikeReconnectResetsBreaker(t *testing.T) {
	st, s := spikeFixture(t)
	for _, kind := range []string{KindConnected, KindTransientDisconnect, KindTransientDisconnect, KindReconnected,
		KindTransientDisconnect, KindTransientDisconnect, KindReconnected} {
		s.Events <- Event{At: time.Now(), Kind: kind}
	}
	v, err := RunSpike(context.Background(), s, st, 0.00002, &bytes.Buffer{})
	require.NoError(t, err)
	require.Equal(t, "survived", v.Outcome)
	require.Equal(t, 4, v.TransientDisconnects)
	require.Equal(t, 2, v.Reconnected)
	require.False(t, s.Permanent(), "a survived run keeps its cached state")
}

func TestRunSpikeQueuedPermanentErrorBeatsExpiredDeadline(t *testing.T) {
	// select picks randomly among ready cases; repeat to catch a lost class.
	for i := 0; i < 50; i++ {
		st, s := spikeFixture(t)
		s.Events <- Event{At: time.Now(), Kind: KindConnected}
		s.Events <- Event{At: time.Now(), Kind: KindPermanentError, Err: Classify(httpclient.ErrCheckpointRequired)}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		v, err := RunSpike(ctx, s, st, 0.0000001, &bytes.Buffer{})
		require.NoError(t, err)
		require.Equal(t, "checkpointed", v.Outcome)
		require.Equal(t, "checkpoint", v.FatalClass)
	}
}

func TestRunSpikePermanentFlagWithoutEventIsAborted(t *testing.T) {
	st, s := spikeFixture(t)
	s.permanent.Store(true)
	v, err := RunSpike(context.Background(), s, st, 0.00002, &bytes.Buffer{})
	require.NoError(t, err)
	require.Equal(t, "aborted", v.Outcome)
	require.Equal(t, "unknown", v.FatalClass)
}

func TestRunSpikeFatalTransientEndsRun(t *testing.T) {
	st, s := spikeFixture(t)
	s.Events <- Event{At: time.Now(), Kind: KindConnected}
	s.Events <- transient(fmt.Errorf("%s: %w", secretText, httpclient.ErrCheckpointRequired))
	var logw bytes.Buffer
	v, err := RunSpike(context.Background(), s, st, 1, &logw)
	require.NoError(t, err)
	require.Equal(t, "checkpointed", v.Outcome)
	require.Equal(t, "checkpoint", v.FatalClass)
	require.Equal(t, 1, v.TransientDisconnects)
	require.Contains(t, logw.String(), `"reason":"checkpoint"`)
}

// A run ended by a fatal-class transient or by the breaker leaves cached state
// that is known bad: Close must drop session.json, as after a permanent_error.
func TestRunSpikeFatalTransientAndBreakerDropCachedState(t *testing.T) {
	cases := map[string][]Event{
		"fatal transient": {{At: time.Now(), Kind: KindConnected}, transient(httpclient.ErrCheckpointRequired)},
		"breaker":         {transient(nil), transient(nil), transient(nil)},
	}
	for name, evts := range cases {
		t.Run(name, func(t *testing.T) {
			st, s := spikeFixture(t)
			require.NoError(t, st.WriteSecret(sessionFile, []byte(`{"Configs":{"BrowserConfigTable":{}}}`)))
			// The client is only for Close; dead proxy, so nothing can reach Meta.
			cs, err := clientSettings("http://127.0.0.1:1")
			require.NoError(t, err)
			s.Client = messagix.NewClient(&cookies.Cookies{Platform: types.Facebook}, state.ProtocolLogger(false), &messagix.Config{ClientSettings: cs})
			for _, e := range evts {
				s.Events <- e
			}

			_, err = RunSpike(context.Background(), s, st, 1, &bytes.Buffer{})
			require.NoError(t, err)
			require.True(t, s.Permanent())
			s.Close(st)
			_, err = st.ReadSecret(sessionFile)
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}
