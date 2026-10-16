package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.mau.fi/mautrix-meta/pkg/messagix/cookies"

	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/auth"
	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/meta"
	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/state"
)

func testStore(t *testing.T) *state.Store {
	t.Helper()
	st, err := state.Open(filepath.Join(t.TempDir(), "fbm"))
	require.NoError(t, err)
	return st
}

// testCookies are the fake cookies from the auth package's fixture.
func testCookies(t *testing.T) *cookies.Cookies {
	t.Helper()
	f, err := os.Open("../../internal/auth/testdata/chrome.json")
	require.NoError(t, err)
	defer f.Close()
	c, err := auth.ParseCookieExport(f)
	require.NoError(t, err)
	return c
}

// socketFailures are the waitSocket results that are not a usable session,
// with the stdout line each must produce.
var socketFailures = map[string]struct {
	perr *meta.ClassifiedError
	want string
}{
	"timeout":         {nil, `{"error_class":"socket_timeout"}`},
	"disconnected":    {nil, `{"error_class":"disconnected"}`},
	"permanent_error": {&meta.ClassifiedError{Class: meta.ClassCheckpoint}, `{"error_class":"checkpoint"}`},
}

func TestReportSocket(t *testing.T) {
	for socket, tc := range socketFailures {
		var out bytes.Buffer
		assert.Equal(t, 1, reportSocket(&out, "x", socket, tc.perr), socket)
		assert.JSONEq(t, tc.want, out.String(), socket)
	}
	var out bytes.Buffer
	assert.Equal(t, 1, reportSocket(&out, "x", "interrupted", nil))
	assert.Empty(t, out.String(), "interrupted goes to stderr only")
}

func TestFinishLoginStoresCookiesOnlyWhenConnected(t *testing.T) {
	export := func(t *testing.T) string {
		p := filepath.Join(t.TempDir(), "cookies.age")
		require.NoError(t, os.WriteFile(p, []byte("not read"), 0o600))
		return p
	}
	for socket, tc := range socketFailures {
		t.Run(socket, func(t *testing.T) {
			st, exp := testStore(t), export(t)
			var out bytes.Buffer
			code := finishLogin(&out, st, &meta.Session{}, testCookies(t), loginOpts{cookies: exp}, socket, tc.perr)
			assert.Equal(t, 1, code)
			assert.JSONEq(t, tc.want, out.String())
			_, err := st.ReadSecret(cookiesFile)
			assert.ErrorIs(t, err, os.ErrNotExist, "cookies.json must not be written")
			assert.FileExists(t, exp, "the export is kept for another try")
		})
	}
	t.Run("interrupted", func(t *testing.T) {
		st, exp := testStore(t), export(t)
		var out bytes.Buffer
		assert.Equal(t, 1, finishLogin(&out, st, &meta.Session{}, testCookies(t), loginOpts{cookies: exp}, "interrupted", nil))
		assert.Empty(t, out.String())
		_, err := st.ReadSecret(cookiesFile)
		assert.ErrorIs(t, err, os.ErrNotExist)
		assert.FileExists(t, exp)
	})
	t.Run("connected", func(t *testing.T) {
		st, exp := testStore(t), export(t)
		var out bytes.Buffer
		assert.Equal(t, 0, finishLogin(&out, st, &meta.Session{}, testCookies(t), loginOpts{cookies: exp}, "connected", nil))
		assert.Contains(t, out.String(), `"socket":"connected"`)
		_, err := st.ReadSecret(cookiesFile)
		assert.NoError(t, err)
		assert.NoFileExists(t, exp)
	})
}

func TestFinishThreadsPrintsRowsOnlyWhenConnected(t *testing.T) {
	s := &meta.Session{Initial: threadsFixture()}
	for socket, tc := range socketFailures {
		var out bytes.Buffer
		assert.Equal(t, 1, finishThreads(&out, testStore(t), s, threadsOpts{format: "json"}, socket, tc.perr), socket)
		assert.JSONEq(t, tc.want, out.String(), "%s: the error line only, no rows", socket)
	}
	var out bytes.Buffer
	assert.Equal(t, 1, finishThreads(&out, testStore(t), s, threadsOpts{format: "json"}, "interrupted", nil))
	assert.Empty(t, out.String())

	out.Reset()
	assert.Equal(t, 0, finishThreads(&out, testStore(t), s, threadsOpts{format: "json"}, "connected", nil))
	assert.Equal(t, len(threadsFixture().LSDeleteThenInsertThread), bytes.Count(out.Bytes(), []byte("\n")))
	assert.NotContains(t, out.String(), "error_class")
}

// A failed re-login must not leave the previous session.json for the spike to
// load: Close never overwrites it with an empty dump.
func TestOpenLoginRemovesStaleSessionState(t *testing.T) {
	st := testStore(t)
	require.NoError(t, st.WriteSecret(sessionFile, []byte(`{"Configs":{"BrowserConfigTable":{}}}`)))
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Open fails at once; the dead proxy keeps any request local.

	s, err := openLogin(ctx, st, testCookies(t), "http://127.0.0.1:1")
	require.Error(t, err)
	assert.Nil(t, s)
	_, err = st.ReadSecret(sessionFile)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

// messagix rotates cookies in place on the *cookies.Cookies it was given.
func rotate(c *cookies.Cookies) {
	vals := c.GetAll()
	vals[cookies.FBCookieXS] = "FAKE-ROTATED-XS"
	c.UpdateValues(vals)
}

func TestCloseSessionSavesRotatedCookies(t *testing.T) {
	st, c := testStore(t), testCookies(t)
	require.NoError(t, writeCookies(st, c))
	rotate(c)

	closeSession(st, &meta.Session{}, c, "threads")
	got, err := loadCookies(st)
	require.NoError(t, err)
	assert.Equal(t, "FAKE-ROTATED-XS", got.Get(cookies.FBCookieXS))
}

func TestCloseSessionAfterPermanentErrorKeepsStoredCookies(t *testing.T) {
	st, c := testStore(t), testCookies(t)
	require.NoError(t, writeCookies(st, c))
	before, err := st.ReadSecret(cookiesFile)
	require.NoError(t, err)
	rotate(c)

	s := &meta.Session{}
	s.MarkPermanent()
	closeSession(st, s, c, "threads")
	after, err := st.ReadSecret(cookiesFile)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}
