package meta_test

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"go.mau.fi/mautrix-meta/pkg/messagix"
	"go.mau.fi/mautrix-meta/pkg/messagix/cookies"

	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/auth"
	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/meta"
	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/state"
)

// deadProxy is a loopback port nothing listens on, so no request can leave the
// machine whichever path Open takes.
const deadProxy = "http://127.0.0.1:1"

func testCookies(t *testing.T) *cookies.Cookies {
	t.Helper()
	f, err := os.Open("../auth/testdata/chrome.json")
	require.NoError(t, err)
	defer f.Close()
	c, err := auth.ParseCookieExport(f)
	require.NoError(t, err)
	return c
}

func TestOpenWithUnusableCachedStateFallsThroughWithoutNetwork(t *testing.T) {
	for name, tc := range map[string]struct{ cache, reason string }{
		"empty object": {`{}`, "load_panic"},
		"too old":      {`{"Timestamp":"2020-01-01T00:00:00Z"}`, "too_old"},
		"not json":     {`nope`, "load_failed"},
	} {
		t.Run(name, func(t *testing.T) {
			st, err := state.Open(filepath.Join(t.TempDir(), "state"))
			require.NoError(t, err)
			require.NoError(t, st.WriteSecret("session.json", []byte(tc.cache)))

			var logs bytes.Buffer
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			s, err := meta.Open(ctx, st, testCookies(t), deadProxy, false, zerolog.New(&logs))

			require.Error(t, err)
			require.Nil(t, s)
			require.NotErrorIs(t, err, messagix.ErrCachedStateTooOld)
			var ce *meta.ClassifiedError
			require.ErrorAs(t, err, &ce)
			require.Contains(t, []meta.Class{meta.ClassTransient, meta.ClassUnknown}, ce.Class)
			require.Contains(t, logs.String(), `"event":"state_discarded"`)
			require.Contains(t, logs.String(), `"reason":"`+tc.reason+`"`)
		})
	}
}

func TestOpenFreshIgnoresCache(t *testing.T) {
	st, err := state.Open(filepath.Join(t.TempDir(), "state"))
	require.NoError(t, err)
	require.NoError(t, st.WriteSecret("session.json", []byte("{}")))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s, err := meta.Open(ctx, st, testCookies(t), deadProxy, true, zerolog.Nop())

	require.Error(t, err)
	require.Nil(t, s)
	var ce *meta.ClassifiedError
	require.ErrorAs(t, err, &ce)
	require.Contains(t, []meta.Class{meta.ClassTransient, meta.ClassUnknown}, ce.Class)
}

// A recording proxy that refuses every CONNECT proves Open honours the proxy:
// the only thing that leaves the process is a CONNECT to the proxy itself.
func TestOpenRoutesThroughProxy(t *testing.T) {
	var mu sync.Mutex
	var targets []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		targets = append(targets, r.Method+" "+r.Host)
		mu.Unlock()
		http.Error(w, "refused", http.StatusBadGateway)
	}))
	defer proxy.Close()

	st, err := state.Open(filepath.Join(t.TempDir(), "state"))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s, err := meta.Open(ctx, st, testCookies(t), proxy.URL, true, zerolog.Nop())

	require.Error(t, err)
	require.Nil(t, s)
	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, targets, "request did not go through the proxy")
	for _, tg := range targets {
		host, _, _ := net.SplitHostPort(tg[len("CONNECT "):])
		require.Contains(t, tg, "CONNECT ")
		require.Contains(t, host, "facebook.com")
	}
}
