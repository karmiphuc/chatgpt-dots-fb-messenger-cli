package auth_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.mau.fi/mautrix-meta/pkg/messagix/cookies"

	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/auth"
	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/browser"
)

var partition = json.RawMessage(`{"topLevelSite":"https://example.com","hasCrossSiteAncestor":false}`)

func fbCookie(name, value, domain string) browser.Cookie {
	return browser.Cookie{Name: name, Value: value, Domain: domain, Path: "/", Expires: -1, Session: true}
}

func TestFromBrowserTakesTheThreeNames(t *testing.T) {
	c, err := auth.FromBrowser([]browser.Cookie{
		fbCookie("c_user", "1234567", ".facebook.com"),
		fbCookie("xs", "fake-xs", ".facebook.com"),
		fbCookie("datr", "fake-datr", ".facebook.com"),
		fbCookie("fr", "fake-fr", ".facebook.com"),
	})
	require.NoError(t, err)
	require.Empty(t, c.GetMissingCookieNames())
	require.Equal(t, int64(1234567), c.GetUserID())
	require.Equal(t, "fake-xs", c.Get(cookies.FBCookieXS))
	require.Equal(t, "fake-datr", c.Get(cookies.MetaCookieDatr))
	require.Empty(t, c.Get(cookies.FBCookieFR), "only c_user, xs and datr are taken")
}

func TestFromBrowserRequiresAllThree(t *testing.T) {
	full := []browser.Cookie{
		fbCookie("c_user", "1", ".facebook.com"),
		fbCookie("xs", "fake-xs", ".facebook.com"),
		fbCookie("datr", "fake-datr", ".facebook.com"),
	}
	for i, missing := range []string{"c_user", "xs", "datr"} {
		cs := append(append([]browser.Cookie{}, full[:i]...), full[i+1:]...)
		_, err := auth.FromBrowser(cs)
		require.ErrorIs(t, err, auth.ErrMissingCookies, missing)
		require.Contains(t, err.Error(), missing)
		require.NotContains(t, err.Error(), "fake-", "error text never carries a value")
	}
}

func TestFromBrowserDropsOtherDomains(t *testing.T) {
	_, err := auth.FromBrowser([]browser.Cookie{
		fbCookie("c_user", "1", ".facebook.com"),
		fbCookie("xs", "fake-xs", ".messenger.com"),
		fbCookie("xs", "fake-xs", "notfacebook.com"),
		fbCookie("datr", "fake-datr", ".facebook.com"),
	})
	require.ErrorIs(t, err, auth.ErrMissingCookies)
	require.Contains(t, err.Error(), "xs")
}

func TestFromBrowserPrefersTheDomainCookie(t *testing.T) {
	c, err := auth.FromBrowser([]browser.Cookie{
		fbCookie("xs", "host-xs", "www.facebook.com"),
		{Name: "xs", Value: "part-xs", Domain: ".facebook.com", Path: "/", Expires: -1, Session: true, PartitionKey: partition},
		fbCookie("xs", "domain-xs", ".facebook.com"),
		fbCookie("c_user", "1", "facebook.com"),
		fbCookie("datr", "host-datr", "www.facebook.com"),
	})
	require.NoError(t, err)
	require.Equal(t, "domain-xs", c.Get(cookies.FBCookieXS), "unpartitioned .facebook.com wins")
	require.Equal(t, int64(1), c.GetUserID(), "bare facebook.com counts as the domain cookie")
	require.Equal(t, "host-datr", c.Get(cookies.MetaCookieDatr), "a host-only cookie is the fallback")
}

func TestFromBrowserPartitionedOnlyIsMissing(t *testing.T) {
	_, err := auth.FromBrowser([]browser.Cookie{
		fbCookie("c_user", "1", ".facebook.com"),
		{Name: "xs", Value: "part-xs", Domain: ".facebook.com", Path: "/", Expires: -1, Session: true, PartitionKey: partition},
		fbCookie("datr", "fake-datr", ".facebook.com"),
	})
	require.ErrorIs(t, err, auth.ErrMissingCookies)
	require.Contains(t, err.Error(), "xs")

	c, err := auth.FromBrowser([]browser.Cookie{
		fbCookie("c_user", "1", ".facebook.com"),
		{Name: "xs", Value: "fake-xs", Domain: ".facebook.com", Path: "/", Expires: -1, Session: true, PartitionKey: json.RawMessage("null")},
		fbCookie("datr", "fake-datr", ".facebook.com"),
	})
	require.NoError(t, err, "a null partitionKey is unpartitioned")
	require.Equal(t, "fake-xs", c.Get(cookies.FBCookieXS))
}

func TestFromBrowserRejectsBadUserID(t *testing.T) {
	for _, v := range []string{"0", "", "abc", "-5", "1e3"} {
		_, err := auth.FromBrowser([]browser.Cookie{
			fbCookie("c_user", v, ".facebook.com"),
			fbCookie("xs", "fake-xs", ".facebook.com"),
			fbCookie("datr", "fake-datr", ".facebook.com"),
		})
		require.ErrorIs(t, err, auth.ErrMissingCookies, "c_user %q", v)
		require.Contains(t, err.Error(), "c_user")
	}
}

func TestFromBrowserDropsExpired(t *testing.T) {
	restore := auth.SetNow(func() time.Time { return time.Unix(2_000_000_100, 0) })
	defer restore()
	expired := fbCookie("xs", "old-xs", ".facebook.com")
	expired.Session, expired.Expires = false, 2_000_000_000.5
	live := fbCookie("xs", "new-xs", "www.facebook.com")
	live.Session, live.Expires = false, 2_000_000_500
	c, err := auth.FromBrowser([]browser.Cookie{
		fbCookie("c_user", "1", ".facebook.com"),
		expired,
		live,
		fbCookie("datr", "fake-datr", ".facebook.com"),
	})
	require.NoError(t, err)
	require.Equal(t, "new-xs", c.Get(cookies.FBCookieXS), "an expired domain cookie loses to a live host-only one")

	_, err = auth.FromBrowser([]browser.Cookie{fbCookie("c_user", "1", ".facebook.com"), expired, fbCookie("datr", "d", ".facebook.com")})
	require.ErrorIs(t, err, auth.ErrMissingCookies)
}
