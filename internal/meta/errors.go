// Package meta is the only code in fbm that talks to Facebook. It wraps a
// messagix client and classifies its errors so callers can stop on a
// checkpoint instead of retrying into a ban.
package meta

import (
	"errors"
	"net"

	"github.com/coder/websocket"

	"go.mau.fi/mautrix-meta/pkg/messagix/dgw"
	"go.mau.fi/mautrix-meta/pkg/messagix/httpclient"
)

// Class is a coarse, stable error category safe to print and to switch on.
type Class string

const (
	ClassCheckpoint  Class = "checkpoint"
	ClassChallenge   Class = "challenge"
	ClassSuspended   Class = "suspended"
	ClassNotLoggedIn Class = "not_logged_in"
	ClassRateLimited Class = "rate_limited"
	ClassTransient   Class = "transient"
	ClassUnknown     Class = "unknown"
)

// ClassifiedError carries the original error for errors.Is/As but prints only
// the class: upstream error strings can embed redirect URLs.
type ClassifiedError struct {
	Class Class
	Err   error
}

func (e *ClassifiedError) Error() string { return string(e.Class) }

func (e *ClassifiedError) Unwrap() error { return e.Err }

// Classify maps err to a Class. Call it only on non-nil errors; Classify(nil)
// returns nil, never a typed-nil error. An error that already is (or wraps) a
// *ClassifiedError is returned unchanged. Checkpoint, challenge, suspended and
// consent are checked before token-invalidated because upstream can report
// both for the same redirect.
func Classify(err error) *ClassifiedError {
	if err == nil {
		return nil
	}
	var ce *ClassifiedError
	if errors.As(err, &ce) {
		return ce
	}
	c := ClassUnknown
	var ne net.Error
	switch {
	case errors.Is(err, httpclient.ErrCheckpointRequired):
		c = ClassCheckpoint
	case errors.Is(err, httpclient.ErrChallengeRequired), errors.Is(err, httpclient.ErrConsentRequired):
		c = ClassChallenge
	case errors.Is(err, httpclient.ErrAccountSuspended):
		c = ClassSuspended
	case errors.Is(err, httpclient.ErrTokenInvalidated):
		c = ClassNotLoggedIn
	case errors.Is(err, httpclient.ErrRateLimited), errors.Is(err, httpclient.ErrTooManyRedirects):
		// Upstream's IsPermanentRequestError treats both as permanent.
		c = ClassRateLimited
	case websocket.CloseStatus(err) == dgw.CloseStatusUnauthorized:
		c = ClassNotLoggedIn
	case errors.As(err, &ne) && ne.Timeout():
		c = ClassTransient
	}
	return &ClassifiedError{Class: c, Err: err}
}

// IsFatal reports whether retrying cannot help and may make things worse.
func IsFatal(c Class) bool {
	switch c {
	case ClassCheckpoint, ClassChallenge, ClassSuspended, ClassNotLoggedIn, ClassRateLimited:
		return true
	}
	return false
}
