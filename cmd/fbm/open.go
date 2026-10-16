package main

import (
	"context"
	"fmt"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix/cookies"

	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/meta"
	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/state"
)

// socketWait is how long login and threads wait for messagix's async Connect.
const socketWait = 30 * time.Second

// waitSocket blocks for the first connected event or a run-ending failure.
// meta.Open returning nil only means Connect was started. The result is
// "connected", "permanent_error" (with its class), "disconnected", "timeout" or
// "interrupted" (ctx done). A transient_disconnect of a fatal class is a
// permanent_error of that class; meta.MaxConsecutiveTransient transients with
// no connected/reconnected between them are "disconnected". Both mark s
// permanent so Close drops the cached state. Other events are skipped.
func waitSocket(ctx context.Context, s *meta.Session, d time.Duration) (string, *meta.ClassifiedError) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	consec := 0
	for {
		select {
		case evt := <-s.Events:
			switch evt.Kind {
			case meta.KindConnected:
				return "connected", nil
			case meta.KindReconnected:
				consec = 0
			case meta.KindTransientDisconnect:
				consec++
				if evt.Err != nil {
					if ce := meta.Classify(evt.Err); meta.IsFatal(ce.Class) {
						s.MarkPermanent()
						return "permanent_error", ce
					}
				}
				if consec >= meta.MaxConsecutiveTransient {
					s.MarkPermanent()
					return "disconnected", nil
				}
			case meta.KindPermanentError:
				if evt.Err == nil {
					return "permanent_error", &meta.ClassifiedError{Class: meta.ClassUnknown}
				}
				return "permanent_error", meta.Classify(evt.Err)
			}
		case <-timer.C:
			return "timeout", nil
		case <-ctx.Done():
			return "interrupted", nil
		}
	}
}

// closeSession closes s, then saves c, which messagix may have rotated in place
// while connected, unless the session saw a permanent error. A save failure is
// a warning: cookies.json still holds the cookies that were loaded.
func closeSession(st *state.Store, s *meta.Session, c *cookies.Cookies, cmd string) {
	s.Close(st)
	if s.Permanent() {
		return
	}
	if err := writeCookies(st, c); err != nil {
		fmt.Fprintf(errOut, "fbm %s: warning: cookies not saved: %v\n", cmd, err)
	}
}
