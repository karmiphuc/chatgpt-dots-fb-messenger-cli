package auth

import "time"

// SetNow overrides the expiry clock and returns a restore func.
func SetNow(f func() time.Time) (restore func()) {
	old := now
	now = f
	return func() { now = old }
}
