package meta

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"go.mau.fi/mautrix-meta/pkg/messagix"
	"go.mau.fi/mautrix-meta/pkg/messagix/cookies"
	"go.mau.fi/mautrix-meta/pkg/messagix/httpclient"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
	"go.mau.fi/mautrix-meta/pkg/messagix/types"

	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/state"
)

func TestClientSettingsWiresProxyTransport(t *testing.T) {
	cs, err := clientSettings("http://127.0.0.1:1")
	require.NoError(t, err)
	require.NotNil(t, cs.HTTPProxy, "ProxyAddress alone does not route traffic; HTTPProxy must be set")

	cs, err = clientSettings("socks5://127.0.0.1:1")
	require.NoError(t, err)
	require.NotNil(t, cs.Dial)

	cs, err = clientSettings("")
	require.NoError(t, err)
	require.Nil(t, cs.HTTPProxy)
}

func TestClientSettingsRejectsBadProxyWithoutEchoingIt(t *testing.T) {
	for _, bad := range []string{"ftp://user:hunter2@host:1", "http://user:hunter2@host:bad port"} {
		_, err := clientSettings(bad)
		var ce *ClassifiedError
		require.ErrorAs(t, err, &ce)
		require.NotContains(t, err.Error(), "hunter2")
		require.NotContains(t, ce.Err.Error(), "hunter2")
	}
}

func recv(t *testing.T, s *Session) Event {
	t.Helper()
	select {
	case e := <-s.Events:
		return e
	case <-time.After(time.Second):
		t.Fatal("no event")
		return Event{}
	}
}

func TestPushDropsTablesButNeverStateEvents(t *testing.T) {
	s := &Session{Events: make(chan Event, eventBuffer)}
	ctx := context.Background()

	for range eventBuffer * 2 {
		s.push(ctx, Event{Kind: KindTable})
	}
	require.Equal(t, eventBuffer-priorityReserve, len(s.Events))
	require.EqualValues(t, eventBuffer+priorityReserve, s.Dropped())

	// With tables saturating the buffer, every state event still fits.
	kinds := []string{KindPermanentError, KindConnected, KindReconnected, KindTransientDisconnect}
	for _, k := range kinds {
		s.push(ctx, Event{Kind: k})
	}
	var got []string
	for len(s.Events) > 0 {
		if e := <-s.Events; e.Kind != KindTable {
			got = append(got, e.Kind)
		}
	}
	require.Equal(t, kinds, got)
}

func TestPushStateEventUnblocksOnCtxCancel(t *testing.T) {
	s := &Session{Events: make(chan Event, 1)}
	s.Events <- Event{Kind: KindConnected}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.push(ctx, Event{Kind: KindPermanentError}); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("push blocked past ctx cancel")
	}
}

func TestHandlerTranslatesEvents(t *testing.T) {
	// Dead proxy: even if upstream starts doing I/O here it cannot reach Meta.
	cs, err := clientSettings("http://127.0.0.1:1")
	require.NoError(t, err)
	cli := messagix.NewClient(&cookies.Cookies{Platform: types.Facebook}, state.ProtocolLogger(false), &messagix.Config{ClientSettings: cs})
	s := &Session{Client: cli, Events: make(chan Event, eventBuffer)}
	h := s.handler(cli)
	ctx := context.Background()

	h(ctx, &messagix.ConnectedEvent{})
	h(ctx, &messagix.ReconnectedEvent{})
	h(ctx, &messagix.TransientDisconnectEvent{})
	h(ctx, &messagix.TransientDisconnectEvent{Err: errors.New("dial")})
	require.False(t, s.permanent.Load())
	h(ctx, &messagix.PermanentErrorEvent{Err: httpclient.ErrCheckpointRequired})
	require.True(t, s.permanent.Load())
	tbl := &table.LSTable{}
	h(ctx, tbl)
	h(ctx, "ignored")

	require.Equal(t, KindConnected, recv(t, s).Kind)
	require.Equal(t, KindReconnected, recv(t, s).Kind)
	e := recv(t, s)
	require.Equal(t, KindTransientDisconnect, e.Kind)
	require.Nil(t, e.Err, "nil upstream error must not become a typed-nil error")
	e = recv(t, s)
	require.Equal(t, KindTransientDisconnect, e.Kind)
	var ce *ClassifiedError
	require.ErrorAs(t, e.Err, &ce)
	require.Equal(t, ClassUnknown, ce.Class)
	e = recv(t, s)
	require.Equal(t, KindPermanentError, e.Kind)
	require.ErrorAs(t, e.Err, &ce)
	require.Equal(t, ClassCheckpoint, ce.Class)
	e = recv(t, s)
	require.Equal(t, KindTable, e.Kind)
	require.Same(t, tbl, e.Table)
	require.False(t, e.At.IsZero())
	require.Empty(t, s.Events)
}

func sessionStore(t *testing.T, content string) *state.Store {
	t.Helper()
	st, err := state.Open(filepath.Join(t.TempDir(), "state"))
	require.NoError(t, err)
	if content != "" {
		require.NoError(t, st.WriteSecret(sessionFile, []byte(content)))
	}
	return st
}

func TestCloseAfterPermanentErrorRemovesCachedState(t *testing.T) {
	st := sessionStore(t, `{"Configs":{"BrowserConfigTable":{}}}`)
	cli := messagix.NewClient(&cookies.Cookies{Platform: types.Facebook}, state.ProtocolLogger(false), &messagix.Config{})
	s := &Session{Client: cli, Events: make(chan Event, eventBuffer)}
	s.handler(cli)(context.Background(), &messagix.PermanentErrorEvent{Err: errors.New("x")})

	s.Close(st)
	_, err := st.ReadSecret(sessionFile)
	require.ErrorIs(t, err, os.ErrNotExist)

	// Nothing to remove is not an error.
	require.NotPanics(t, func() { s.Close(st) })
}

func TestCloseWithoutPermanentErrorKeepsExistingStateWhenDumpIsEmpty(t *testing.T) {
	st := sessionStore(t, `{"Configs":{"BrowserConfigTable":{}}}`)
	cli := messagix.NewClient(&cookies.Cookies{Platform: types.Facebook}, state.ProtocolLogger(false), &messagix.Config{})
	s := &Session{Client: cli, Events: make(chan Event, eventBuffer)}

	s.Close(st)
	raw, err := st.ReadSecret(sessionFile)
	require.NoError(t, err)
	require.JSONEq(t, `{"Configs":{"BrowserConfigTable":{}}}`, string(raw))
}

// On the cached path LoadState marks the client synced, so a save during Open
// would re-stamp the old state. Open must leave session.json untouched.
func TestOpenCachedPathDoesNotRewriteState(t *testing.T) {
	st := sessionStore(t, `{"Configs":{"BrowserConfigTable":{}}}`)
	p := filepath.Join(st.Dir, sessionFile)
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	require.NoError(t, os.Chtimes(p, old, old))

	cs := &cookies.Cookies{Platform: types.Facebook}
	connected := false
	s, err := open(context.Background(), st, cs, "http://127.0.0.1:1", false, zerolog.Nop(),
		func(*messagix.Client, context.Context) error { connected = true; return nil })

	require.NoError(t, err)
	require.True(t, connected, "cached state must be accepted, not fall through to LoadMessagesPage")
	require.Nil(t, s.Initial)
	require.Nil(t, s.Me)
	info, err := os.Stat(p)
	require.NoError(t, err)
	require.True(t, info.ModTime().Equal(old), "session.json was rewritten by Open")
}
