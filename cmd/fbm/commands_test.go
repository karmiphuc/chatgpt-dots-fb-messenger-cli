package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/meta"
	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/state"
)

func TestParseLoginFlags(t *testing.T) {
	o, err := parseLoginFlags([]string{"--keygen"}, io.Discard)
	require.NoError(t, err)
	assert.True(t, o.keygen)

	o, err = parseLoginFlags([]string{"--cookies", "/x/c.json", "--proxy", "http://p:1", "--keep", "--owner-cleared"}, io.Discard)
	require.NoError(t, err)
	assert.Equal(t, loginOpts{cookies: "/x/c.json", proxy: "http://p:1", keep: true, ownerCleared: true, timeout: 15 * time.Minute}, o)

	o, err = parseLoginFlags([]string{"--browser"}, io.Discard)
	require.NoError(t, err)
	assert.Equal(t, loginOpts{browser: true, timeout: 15 * time.Minute}, o)

	o, err = parseLoginFlags([]string{"--browser", "--timeout", "5m", "--owner-cleared", "--allow-no-sandbox", "--proxy", "http://p:1"}, io.Discard)
	require.NoError(t, err)
	assert.Equal(t, loginOpts{browser: true, timeout: 5 * time.Minute, ownerCleared: true, allowNoSandbox: true, proxy: "http://p:1"}, o)

	_, err = parseLoginFlags(nil, io.Discard)
	assert.Error(t, err, "needs --keygen, --cookies or --browser")
	for _, bad := range [][]string{
		{"--keygen", "--cookies", "x"},
		{"--keygen", "--browser"},
		{"--cookies", "x", "--browser"},
		{"--keygen", "--owner-cleared"},
		{"--cookies", "x", "--timeout", "1m"},
		{"--cookies", "x", "--allow-no-sandbox"},
		{"--browser", "--keep"},
		{"--browser", "--timeout", "0s"},
		{"--browser", "--timeout", "500ms"},
	} {
		_, err = parseLoginFlags(bad, io.Discard)
		assert.Error(t, err, "%v", bad)
	}
	_, err = parseLoginFlags([]string{"--bogus"}, io.Discard)
	assert.Error(t, err)
	_, err = parseLoginFlags([]string{"--cookies", "x", "extra"}, io.Discard)
	assert.Error(t, err, "positional args rejected")
}

func TestParseThreadsFlags(t *testing.T) {
	o, err := parseThreadsFlags(nil, io.Discard)
	require.NoError(t, err)
	assert.Equal(t, threadsOpts{limit: 50, format: "json"}, o)

	o, err = parseThreadsFlags([]string{"--limit", "5", "--format", "text", "--proxy", "http://p:1"}, io.Discard)
	require.NoError(t, err)
	assert.Equal(t, threadsOpts{limit: 5, format: "text", proxy: "http://p:1"}, o)

	_, err = parseThreadsFlags([]string{"--format", "yaml"}, io.Discard)
	assert.Error(t, err)
	_, err = parseThreadsFlags([]string{"--limit", "-1"}, io.Discard)
	assert.Error(t, err)
}

func TestParseStatusFlags(t *testing.T) {
	require.NoError(t, parseStatusFlags(nil, io.Discard))
	assert.Error(t, parseStatusFlags([]string{"--nope"}, io.Discard))
	assert.Error(t, parseStatusFlags([]string{"extra"}, io.Discard))
}

func TestStatusObjectHasNoSecrets(t *testing.T) {
	const fakeCookie = "FAKE_XS_COOKIE_VALUE_0123456789"
	const fakeSession = "FAKE_SESSION_STATE_TOKEN_abcdef"
	const fakeKey = "AGE-SECRET-KEY-FAKEFAKEFAKE"
	st, err := state.Open(filepath.Join(t.TempDir(), "fbm"))
	require.NoError(t, err)

	// Empty state dir.
	var buf bytes.Buffer
	require.NoError(t, writeJSON(&buf, buildStatus(st)))
	assert.JSONEq(t, `{"cookies":{"present":false,"age_s":0},"session":{"present":false,"age_s":0,"size":0},"key":{"present":false},"last_verdict":null}`, buf.String())

	require.NoError(t, st.WriteSecret("cookies.json", []byte(`{"xs":"`+fakeCookie+`"}`)))
	require.NoError(t, st.WriteSecret("session.json", []byte(fakeSession)))
	require.NoError(t, st.WriteSecret("fbm.key", []byte(fakeKey)))
	require.NoError(t, st.WriteSecret("spike-verdict.json", []byte(`{"verdict":"go"}`)))
	old := time.Now().Add(-90 * time.Second)
	require.NoError(t, os.Chtimes(filepath.Join(st.Dir, "cookies.json"), old, old))

	buf.Reset()
	require.NoError(t, writeJSON(&buf, buildStatus(st)))
	out := buf.String()
	for _, secret := range []string{fakeCookie, fakeSession, fakeKey} {
		assert.NotContains(t, out, secret)
	}

	var got struct {
		Cookies struct {
			Present bool  `json:"present"`
			AgeS    int64 `json:"age_s"`
		} `json:"cookies"`
		LastVerdict map[string]string `json:"last_verdict"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.True(t, got.Cookies.Present)
	assert.GreaterOrEqual(t, got.Cookies.AgeS, int64(90))
	assert.Equal(t, "go", got.LastVerdict["verdict"])
	assert.Contains(t, out, `"size":31`, "session size is the byte length")
}

func TestStatusIgnoresUnparseableVerdict(t *testing.T) {
	st, err := state.Open(filepath.Join(t.TempDir(), "fbm"))
	require.NoError(t, err)
	require.NoError(t, st.WriteSecret("spike-verdict.json", []byte("not json")))
	var buf bytes.Buffer
	require.NoError(t, writeJSON(&buf, buildStatus(st)))
	assert.Contains(t, buf.String(), `"last_verdict":null`)
}

func TestWaitSocket(t *testing.T) {
	newSess := func(evts ...meta.Event) *meta.Session {
		s := &meta.Session{Events: make(chan meta.Event, 8)}
		for _, e := range evts {
			s.Events <- e
		}
		return s
	}
	ctx := context.Background()

	t.Run("connected, skipping other kinds", func(t *testing.T) {
		s := newSess(meta.Event{Kind: meta.KindTable}, meta.Event{Kind: meta.KindTransientDisconnect}, meta.Event{Kind: meta.KindConnected})
		sock, perr := waitSocket(ctx, s, time.Second)
		assert.Equal(t, "connected", sock)
		assert.Nil(t, perr)
	})
	t.Run("permanent error classified", func(t *testing.T) {
		s := newSess(meta.Event{Kind: meta.KindPermanentError, Err: &meta.ClassifiedError{Class: meta.ClassCheckpoint}})
		sock, perr := waitSocket(ctx, s, time.Second)
		assert.Equal(t, "permanent_error", sock)
		require.NotNil(t, perr)
		assert.Equal(t, meta.ClassCheckpoint, perr.Class)
	})
	t.Run("permanent error without cause is unknown", func(t *testing.T) {
		s := newSess(meta.Event{Kind: meta.KindPermanentError})
		_, perr := waitSocket(ctx, s, time.Second)
		require.NotNil(t, perr)
		assert.Equal(t, meta.ClassUnknown, perr.Class)
	})
	transient := func(c meta.Class) meta.Event {
		return meta.Event{Kind: meta.KindTransientDisconnect, Err: &meta.ClassifiedError{Class: c}}
	}
	t.Run("fatal transient is a permanent error of its class", func(t *testing.T) {
		s := newSess(transient(meta.ClassCheckpoint), meta.Event{Kind: meta.KindConnected})
		sock, perr := waitSocket(ctx, s, time.Second)
		assert.Equal(t, "permanent_error", sock)
		require.NotNil(t, perr)
		assert.Equal(t, meta.ClassCheckpoint, perr.Class)
		assert.True(t, s.Permanent(), "Close must drop the cached state")
	})
	t.Run("three transients in a row are disconnected", func(t *testing.T) {
		s := newSess(meta.Event{Kind: meta.KindTransientDisconnect}, transient(meta.ClassUnknown), transient(meta.ClassTransient),
			meta.Event{Kind: meta.KindConnected})
		sock, perr := waitSocket(ctx, s, time.Second)
		assert.Equal(t, "disconnected", sock)
		assert.Nil(t, perr)
		assert.True(t, s.Permanent(), "Close must drop the cached state")
	})
	t.Run("transient then connected", func(t *testing.T) {
		s := newSess(transient(meta.ClassUnknown), meta.Event{Kind: meta.KindConnected})
		sock, perr := waitSocket(ctx, s, time.Second)
		assert.Equal(t, "connected", sock)
		assert.Nil(t, perr)
		assert.False(t, s.Permanent())
	})
	t.Run("reconnected resets the breaker", func(t *testing.T) {
		s := newSess(transient(meta.ClassUnknown), transient(meta.ClassUnknown), meta.Event{Kind: meta.KindReconnected},
			transient(meta.ClassUnknown), transient(meta.ClassUnknown), meta.Event{Kind: meta.KindConnected})
		sock, _ := waitSocket(ctx, s, time.Second)
		assert.Equal(t, "connected", sock)
	})
	t.Run("timeout", func(t *testing.T) {
		sock, perr := waitSocket(ctx, newSess(), 20*time.Millisecond)
		assert.Equal(t, "timeout", sock)
		assert.Nil(t, perr)
	})
	t.Run("interrupted", func(t *testing.T) {
		c, cancel := context.WithCancel(ctx)
		cancel()
		sock, perr := waitSocket(c, newSess(), time.Second)
		assert.Equal(t, "interrupted", sock)
		assert.Nil(t, perr)
	})
}

func TestReportErrorPrintsClassOnly(t *testing.T) {
	var buf bytes.Buffer
	code := reportError(&buf, &meta.ClassifiedError{Class: meta.ClassCheckpoint})
	assert.Equal(t, 1, code)
	assert.JSONEq(t, `{"error_class":"checkpoint"}`, buf.String())
}
