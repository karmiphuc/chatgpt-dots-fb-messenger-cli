package meta_test

import (
	"context"
	"fmt"
	"net"
	"testing"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	"go.mau.fi/mautrix-meta/pkg/messagix/dgw"
	"go.mau.fi/mautrix-meta/pkg/messagix/httpclient"

	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/meta"
)

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

var _ net.Error = timeoutErr{}

func TestClassify(t *testing.T) {
	redirected := func(typ error) error {
		return fmt.Errorf("failed to load inbox: %w",
			httpclient.RedirectedError{Type: typ, URL: "https://example.invalid/secret-path"})
	}
	cases := []struct {
		name string
		err  error
		want meta.Class
	}{
		{"checkpoint", fmt.Errorf("x: %w", httpclient.ErrCheckpointRequired), meta.ClassCheckpoint},
		{"checkpoint via redirect", redirected(httpclient.ErrCheckpointRequired), meta.ClassCheckpoint},
		{"challenge", fmt.Errorf("x: %w", httpclient.ErrChallengeRequired), meta.ClassChallenge},
		{"challenge via redirect", redirected(httpclient.ErrChallengeRequired), meta.ClassChallenge},
		{"consent", fmt.Errorf("x: %w", httpclient.ErrConsentRequired), meta.ClassChallenge},
		{"consent via redirect", redirected(httpclient.ErrConsentRequired), meta.ClassChallenge},
		{"suspended", fmt.Errorf("x: %w", httpclient.ErrAccountSuspended), meta.ClassSuspended},
		{"suspended via redirect", redirected(httpclient.ErrAccountSuspended), meta.ClassSuspended},
		{"token invalidated", fmt.Errorf("x: %w", httpclient.ErrTokenInvalidated), meta.ClassNotLoggedIn},
		{"token invalidated redirect", fmt.Errorf("x: %w", httpclient.ErrTokenInvalidatedRedirect), meta.ClassNotLoggedIn},
		{"user id zero", fmt.Errorf("x: %w", httpclient.ErrUserIDIsZero), meta.ClassNotLoggedIn},
		{"rate limited", fmt.Errorf("x: %w", httpclient.ErrRateLimited), meta.ClassRateLimited},
		{"too many redirects", fmt.Errorf("x: %w", httpclient.ErrTooManyRedirects), meta.ClassRateLimited},
		{"ws 4003", fmt.Errorf("x: %w", websocket.CloseError{Code: dgw.CloseStatusUnauthorized}), meta.ClassNotLoggedIn},
		{"ws other close", fmt.Errorf("x: %w", websocket.CloseError{Code: websocket.StatusGoingAway}), meta.ClassUnknown},
		{"net timeout", fmt.Errorf("x: %w", timeoutErr{}), meta.ClassTransient},
		{"deadline exceeded", fmt.Errorf("x: %w", context.DeadlineExceeded), meta.ClassTransient},
		{"plain", fmt.Errorf("boom"), meta.ClassUnknown},
		// A checkpoint redirect must win over the token-invalidated sentinel.
		{"checkpoint beats token", fmt.Errorf("%w / %w", httpclient.ErrTokenInvalidated, httpclient.ErrCheckpointRequired), meta.ClassCheckpoint},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := meta.Classify(tc.err)
			require.NotNil(t, got)
			require.Equal(t, tc.want, got.Class)
			require.ErrorIs(t, got, tc.err)
		})
	}
}

func TestClassifyNilAndIdempotent(t *testing.T) {
	require.Nil(t, meta.Classify(nil))

	first := meta.Classify(httpclient.ErrCheckpointRequired)
	require.Same(t, first, meta.Classify(first))
	wrapped := fmt.Errorf("outer: %w", first)
	require.Same(t, first, meta.Classify(wrapped))
}

func TestClassifiedErrorMessageHidesCause(t *testing.T) {
	err := httpclient.RedirectedError{Type: httpclient.ErrConsentRequired, URL: "https://example.invalid/secret-path"}
	got := meta.Classify(err)
	require.Equal(t, "challenge", got.Error())
	require.NotContains(t, got.Error(), "secret-path")
	require.NotContains(t, fmt.Sprintf("%v %s", got, got), "secret-path")
}

func TestIsFatal(t *testing.T) {
	for _, c := range []meta.Class{meta.ClassCheckpoint, meta.ClassChallenge, meta.ClassSuspended, meta.ClassNotLoggedIn, meta.ClassRateLimited} {
		require.True(t, meta.IsFatal(c), c)
	}
	for _, c := range []meta.Class{meta.ClassTransient, meta.ClassUnknown, ""} {
		require.False(t, meta.IsFatal(c), c)
	}
}
