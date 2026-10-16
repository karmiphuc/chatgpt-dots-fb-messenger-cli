package state

import (
	"os"

	"github.com/rs/zerolog"
)

// NewLogger is fbm's own logger: stderr, Info. Callers must never attach
// cookie, session or token values; the only string fields allowed are
// event, reason, thread_count and elapsed. debugProtocol is accepted for call-site
// symmetry only; protocol verbosity is ProtocolLogger's job.
func NewLogger(debugProtocol bool) zerolog.Logger {
	return zerolog.New(os.Stderr).Level(zerolog.InfoLevel).With().Timestamp().Logger()
}

// ProtocolLogger is the logger handed to messagix.NewClient. It is off by
// default because upstream debug paths log whole response bodies.
func ProtocolLogger(debug bool) zerolog.Logger {
	if !debug {
		return zerolog.Nop()
	}
	return zerolog.New(os.Stderr).Level(zerolog.DebugLevel).With().Timestamp().Logger()
}
