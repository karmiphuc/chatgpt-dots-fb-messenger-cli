package auth_test

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/auth"
	"go.mau.fi/mautrix-meta/pkg/messagix/cookies"
)

func open(t *testing.T, path string) io.Reader {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { f.Close() })
	return f
}

func TestParseCookieExport_JSONArrayKeepsOnlyMetaNames(t *testing.T) {
	c, err := auth.ParseCookieExport(open(t, "testdata/chrome.json"))
	require.NoError(t, err)
	require.Len(t, c.GetMissingCookieNames(), 0)
	require.Equal(t, int64(1234567), c.GetUserID())
	require.Empty(t, c.Get("unrelated_cookie"))
}

func TestParseCookieExport_NetscapeHttpOnlyPrefix(t *testing.T) {
	c, err := auth.ParseCookieExport(open(t, "testdata/netscape.txt"))
	require.NoError(t, err)
	require.NotEmpty(t, c.Get(cookies.FBCookieXS))
	require.NotEmpty(t, c.Get(cookies.MetaCookieDatr))
	require.Equal(t, int64(1234567), c.GetUserID())
	require.Empty(t, c.Get(cookies.FBCookieFR), "expired fr must be dropped")
}

func TestParseCookieExport_MissingDatr(t *testing.T) {
	_, err := auth.ParseCookieExport(strings.NewReader(`[{"name":"xs","value":"a"},{"name":"c_user","value":"1"}]`))
	require.ErrorIs(t, err, auth.ErrMissingCookies)
	require.Contains(t, err.Error(), "datr")
}

func TestParseCookieExport_JSONIgnoresOtherMetaDomains(t *testing.T) {
	c, err := auth.ParseCookieExport(strings.NewReader(`[
		{"name":"xs","value":"fb-xs","domain":".facebook.com"},
		{"name":"c_user","value":"1","domain":".facebook.com"},
		{"name":"datr","value":"fb-datr","domain":".facebook.com"},
		{"name":"datr","value":"messenger-datr","domain":".messenger.com"},
		{"name":"sb","value":"ig-sb","domain":".instagram.com"},
		{"name":"sb","value":"www-sb","domain":"www.facebook.com"},
		{"name":"xs","value":"evil-xs","domain":"notfacebook.com"}
	]`))
	require.NoError(t, err)
	require.Equal(t, "fb-datr", c.Get(cookies.MetaCookieDatr))
	require.Equal(t, "fb-xs", c.Get(cookies.FBCookieXS))
	require.Equal(t, "www-sb", c.Get(cookies.FBCookieSB))
}

func TestParseCookieExport_JSONExpiredXSIsMissing(t *testing.T) {
	restore := auth.SetNow(func() time.Time { return time.Unix(2_000_000_100, 0) })
	defer restore()
	in := `[
		{"name":"xs","value":"a","domain":".facebook.com","expirationDate":2000000000.5},
		{"name":"c_user","value":"1","domain":".facebook.com","expirationDate":2000000000.5,"session":true},
		{"name":"datr","value":"d","domain":".facebook.com"}
	]`
	_, err := auth.ParseCookieExport(strings.NewReader(in))
	require.ErrorIs(t, err, auth.ErrMissingCookies)
	require.Contains(t, err.Error(), "xs")
	require.NotContains(t, err.Error(), "c_user")

	restore()
	restore = auth.SetNow(func() time.Time { return time.Unix(1_999_999_000, 0) })
	defer restore()
	_, err = auth.ParseCookieExport(strings.NewReader(in))
	require.NoError(t, err)
}

func TestParseCookieExport_JSONObjectShape(t *testing.T) {
	c, err := auth.ParseCookieExport(strings.NewReader(`{"xs":"a","c_user":"1","datr":"d","other":"x"}`))
	require.NoError(t, err)
	require.Equal(t, "a", c.Get(cookies.FBCookieXS))
	require.Equal(t, int64(1), c.GetUserID())
	require.Empty(t, c.Get("other"))
}
