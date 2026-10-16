package state_test

import (
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/state"
)

func TestNewLoggerIsInfo(t *testing.T) {
	l := state.NewLogger(false)
	require.Equal(t, zerolog.InfoLevel, l.GetLevel())
}

func TestProtocolLoggerOffByDefault(t *testing.T) {
	off := state.ProtocolLogger(false)
	require.Equal(t, zerolog.Disabled, off.GetLevel())
	on := state.ProtocolLogger(true)
	require.Equal(t, zerolog.DebugLevel, on.GetLevel())
}
