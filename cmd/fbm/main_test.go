package main

import (
	"bytes"
	"io"
	"os"
	"testing"

	"github.com/rs/zerolog"
	zlog "github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
)

// messagix's lightspeed decoder and table code log raw stored-procedure
// arguments through zerolog's global logger, so it must be off before any
// command runs.
func TestUpstreamGlobalLoggerIsDisabled(t *testing.T) {
	assert.Equal(t, zerolog.Disabled, zlog.Logger.GetLevel())
}

// TestMain keeps expected-path diagnostics out of the test output. A test that
// asserts on them uses captureErr.
func TestMain(m *testing.M) {
	errOut = io.Discard
	os.Exit(m.Run())
}

// captureErr points errOut at a buffer for one test.
func captureErr(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := errOut
	errOut = &buf
	t.Cleanup(func() { errOut = old })
	return &buf
}
