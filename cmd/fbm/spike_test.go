package main

import (
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/meta"
)

func TestParseSpikeFlags(t *testing.T) {
	o, err := parseSpikeFlags(nil, io.Discard)
	require.NoError(t, err)
	assert.Equal(t, spikeOpts{hours: 48}, o)

	o, err = parseSpikeFlags([]string{"--hours", "0.1", "--proxy", "http://p:1"}, io.Discard)
	require.NoError(t, err)
	assert.Equal(t, spikeOpts{hours: 0.1, proxy: "http://p:1"}, o)

	for _, bad := range []string{"0", "-1", "NaN", "+Inf", "1e9"} {
		_, err = parseSpikeFlags([]string{"--hours", bad}, io.Discard)
		assert.Error(t, err, "--hours %s", bad)
	}
	_, err = parseSpikeFlags([]string{"extra"}, io.Discard)
	assert.Error(t, err)
	_, err = parseSpikeFlags([]string{"--bogus"}, io.Discard)
	assert.Error(t, err)
}

func TestSpikeExit(t *testing.T) {
	assert.Equal(t, 0, spikeExit(meta.OutcomeSurvived))
	assert.Equal(t, 3, spikeExit(meta.OutcomeCheckpointed))
	assert.Equal(t, 3, spikeExit(meta.OutcomeLoggedOut))
	assert.Equal(t, 3, spikeExit(meta.OutcomeDisconnected))
	assert.Equal(t, 1, spikeExit(meta.OutcomeAborted))
	assert.Equal(t, 1, spikeExit("anything else"))
}

func TestVerdictFileNameMatchesMeta(t *testing.T) {
	assert.Equal(t, meta.VerdictFile, verdictFile, "fbm status must read the file the spike writes")
}
