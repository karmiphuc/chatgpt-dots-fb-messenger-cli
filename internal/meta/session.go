package meta

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/exhttp"

	"go.mau.fi/mautrix-meta/pkg/messagix"
	"go.mau.fi/mautrix-meta/pkg/messagix/cookies"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
	"go.mau.fi/mautrix-meta/pkg/messagix/types"

	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/state"
)

const (
	sessionFile = "session.json"

	// Event kinds.
	KindConnected           = "connected"
	KindReconnected         = "reconnected"
	KindTransientDisconnect = "transient_disconnect"
	KindPermanentError      = "permanent_error"
	KindTable               = "table"

	eventBuffer = 64
	// priorityReserve slots of the buffer are never taken by table events, so
	// connection-state events always have room while the consumer lags.
	priorityReserve = 8
)

// Event is a translated messagix event. Err is a *ClassifiedError for
// transient_disconnect (when upstream supplied one) and permanent_error.
type Event struct {
	At   time.Time
	Kind string
	Err  error
	// Table is set for Kind == KindTable.
	Table *table.LSTable
}

// Session is an open messagix connection. Events is never closed: messagix
// can still emit while Disconnect runs.
type Session struct {
	Client *messagix.Client
	// Me and Initial are set only when Open loaded the messages page (fresh,
	// or no usable cached state). After a cached-state connect they are nil.
	Me      types.UserInfo
	Initial *table.LSTable
	Events  chan Event

	log     zerolog.Logger
	dropped atomic.Int64
	// permanent is set once a permanent_error event was seen; Close then drops
	// the cached state instead of saving it.
	permanent atomic.Bool
}

// Dropped is the number of table events discarded because the consumer fell
// behind. Connection-state events are never dropped.
func (s *Session) Dropped() int64 { return s.dropped.Load() }

// Permanent reports whether the session saw a permanent error, or was marked
// by MarkPermanent. Its cached state is then known bad and Close drops it.
func (s *Session) Permanent() bool { return s.permanent.Load() }

// MarkPermanent records a failure that ended the session as if it were a
// permanent error, e.g. a fatal-class transient disconnect or the
// transient-disconnect breaker, so Close drops the cached state.
func (s *Session) MarkPermanent() { s.permanent.Store(true) }

// push queues evt without blocking the messagix goroutine on a slow consumer.
// A table event is dropped (newest) once the buffer is down to the priority
// reserve. Any other event is sent blocking, bounded by ctx; the reserve makes
// that wait practically unreachable.
func (s *Session) push(ctx context.Context, evt Event) {
	if evt.Kind == KindTable {
		if len(s.Events) >= eventBuffer-priorityReserve {
			s.dropped.Add(1)
			return
		}
		select {
		case s.Events <- evt:
		default:
			s.dropped.Add(1)
		}
		return
	}
	select {
	case s.Events <- evt:
	case <-ctx.Done():
	}
}

// handler translates messagix events. Unknown event types are ignored.
func (s *Session) handler(cli *messagix.Client) messagix.EventHandler {
	return func(ctx context.Context, raw any) {
		now := time.Now()
		switch evt := raw.(type) {
		case *messagix.ConnectedEvent:
			s.push(ctx, Event{At: now, Kind: KindConnected})
		case *messagix.ReconnectedEvent:
			s.push(ctx, Event{At: now, Kind: KindReconnected})
		case *messagix.TransientDisconnectEvent:
			e := Event{At: now, Kind: KindTransientDisconnect}
			if evt.Err != nil {
				e.Err = Classify(evt.Err)
			}
			s.push(ctx, e)
		case *messagix.PermanentErrorEvent:
			s.permanent.Store(true)
			e := Event{At: now, Kind: KindPermanentError}
			if evt.Err != nil {
				e.Err = Classify(evt.Err)
			}
			s.push(ctx, e)
		case *table.LSTable:
			s.push(ctx, Event{At: now, Kind: KindTable, Table: evt})
			// Only advances local sync cursors; makes no request.
			cli.PostHandlePublishResponse(evt)
		}
	}
}

// clientSettings builds the messagix HTTP settings. Setting
// ClientSettings.ProxyAddress alone is a no-op in messagix: only WithProxy wires
// the transport, so a bare field would send traffic direct to Meta. An empty
// proxy means direct. The parse error is dropped because it can echo proxy
// credentials.
func clientSettings(proxy string) (exhttp.ClientSettings, error) {
	cs := exhttp.ClientSettings{}.WithDialTimeout(10 * time.Second)
	if proxy == "" {
		return cs, nil
	}
	cs, err := cs.WithProxy(proxy)
	if err != nil {
		return cs, &ClassifiedError{Class: ClassUnknown, Err: errors.New("invalid proxy address")}
	}
	return cs, nil
}

// Open builds a messagix client from cookies and connects it. log is fbm's own
// logger (state.NewLogger); it is never handed to messagix, which always gets
// state.ProtocolLogger(false).
//
// ctx must live as long as the session: Connect runs messagix's reconnect loop
// under it, so a short timeout ctx would silently end the connection. Callers
// bound the dial with their own timer, not by passing a deadline here.
//
// With fresh=false and a loadable cached session.json, the messages page is not
// loaded and Initial and Me stay nil. fresh=true ignores the cache and always
// loads the page so Initial is populated. Errors are *ClassifiedError.
func Open(ctx context.Context, st *state.Store, c *cookies.Cookies, proxy string, fresh bool, log zerolog.Logger) (*Session, error) {
	return open(ctx, st, c, proxy, fresh, log, (*messagix.Client).Connect)
}

// open is Open with the connect step injectable, so tests can drive the cached
// path to success without a network.
func open(ctx context.Context, st *state.Store, c *cookies.Cookies, proxy string, fresh bool, log zerolog.Logger,
	connect func(*messagix.Client, context.Context) error) (*Session, error) {
	settings, err := clientSettings(proxy)
	if err != nil {
		return nil, err
	}
	newClient := func() *messagix.Client {
		return messagix.NewClient(c, state.ProtocolLogger(false), &messagix.Config{ClientSettings: settings})
	}
	cli := newClient()
	s := &Session{Events: make(chan Event, eventBuffer), log: log}

	// A cached state skips the messages page, as upstream connectWithCache does.
	cached := !fresh && loadCached(st, cli, log)
	if !fresh && !cached {
		// A rejected LoadState can leave the client half-written; start clean.
		cli = newClient()
	}
	s.Client = cli
	if !cached {
		me, tbl, err := cli.LoadMessagesPage(ctx)
		if err != nil {
			return nil, Classify(err)
		}
		s.Me, s.Initial = me, tbl
	}

	cli.SetEventHandler(s.handler(cli))
	if err := connect(cli, ctx); err != nil {
		return nil, Classify(err)
	}
	// No save here: on the cached path LoadState marks the client synced, so a
	// dump now would re-stamp the old state as fresh and defeat MaxCachedStateAge.
	// State is persisted by Close, after a connection actually ran.
	return s, nil
}

// loadCached reports whether session.json was read and accepted by messagix.
// Any failure is logged as state_discarded and falls through to a fresh load.
// Upstream LoadState dereferences nil on a state without configs (e.g. "{}"),
// so a panic is treated as a rejection too.
func loadCached(st *state.Store, cli *messagix.Client, log zerolog.Logger) (ok bool) {
	raw, err := st.ReadSecret(sessionFile)
	if err != nil {
		return false
	}
	reason := ""
	defer func() {
		if r := recover(); r != nil {
			ok, reason = false, "load_panic"
		}
		if !ok {
			log.Info().Str("event", "state_discarded").Str("reason", reason).Msg("cached session discarded")
		}
	}()
	if err := cli.LoadState(raw); err != nil {
		reason = "load_failed"
		if errors.Is(err, messagix.ErrCachedStateTooOld) {
			reason = "too_old"
		}
		return false
	}
	return true
}

// saveState persists messagix's session state. DumpState returns nothing until
// a sync has completed (or a cached state was loaded); an empty dump never
// overwrites a good file. Failure is a warning: persisting the cache is optional.
// Call it only after Disconnect: the connection loop writes the sync store that
// DumpState marshals, unlocked.
func (s *Session) saveState(st *state.Store) {
	raw, err := s.Client.DumpState()
	if err != nil || len(raw) == 0 {
		return
	}
	if err := st.WriteSecret(sessionFile, raw); err != nil {
		s.log.Warn().Str("event", "state_save_failed").Msg("session state not persisted")
	}
}

// Close disconnects, then persists session state. Disconnect comes first (it
// waits for the connection loop to stop) so the dump does not race the loop.
// After a permanent_error the cached state is known bad: it is removed, not saved.
func (s *Session) Close(st *state.Store) {
	if s == nil || s.Client == nil {
		return
	}
	s.Client.Disconnect()
	if s.permanent.Load() {
		if err := st.Remove(sessionFile); err != nil {
			s.log.Warn().Str("event", "state_remove_failed").Msg("bad session state not removed")
		}
		return
	}
	s.saveState(st)
}
