package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mau.fi/mautrix-meta/pkg/messagix/table"
)

func TestThreadKind(t *testing.T) {
	cases := map[table.ThreadType]string{
		table.ONE_TO_ONE:                   "dm",
		table.TINCAN_ONE_TO_ONE:            "dm",
		table.ENCRYPTED_OVER_WA_ONE_TO_ONE: "dm",
		table.GROUP_THREAD:                 "group",
		table.ENCRYPTED_OVER_WA_GROUP:      "group",
		table.TINCAN_GROUP_DISAPPEARING:    "group",
		table.CARRIER_MESSAGING_GROUP:      "group",
		table.ThreadType(9999):             "other",
	}
	for tt, want := range cases {
		assert.Equal(t, want, threadKind(tt), "thread type %d", tt)
	}
}

func TestWriteJSONOneLine(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, writeJSON(&buf, map[string]any{"name": "a <b> & c", "n": 1}))
	out := buf.String()
	assert.True(t, strings.HasSuffix(out, "\n"))
	assert.Equal(t, 1, strings.Count(out, "\n"), "exactly one line")
	assert.Contains(t, out, "a <b> & c", "no HTML escaping")
}

func threadsFixture() *table.LSTable {
	return &table.LSTable{LSDeleteThenInsertThread: []*table.LSDeleteThenInsertThread{
		{ThreadKey: 1, ThreadType: table.ONE_TO_ONE, LastActivityTimestampMs: 1000, SyncGroup: 1},
		{ThreadKey: 2, ThreadType: table.GROUP_THREAD, ThreadName: "crew\tteam\nx", LastActivityTimestampMs: 3000, SyncGroup: 1},
		{ThreadKey: 9007199254740993, ThreadType: table.ENCRYPTED_OVER_WA_ONE_TO_ONE, LastActivityTimestampMs: 2000, SyncGroup: 95},
	}}
}

func TestThreadRowsSortedAndLimited(t *testing.T) {
	rows := threadRows(threadsFixture(), 0)
	require.Len(t, rows, 3)
	assert.Equal(t, []string{"2", "9007199254740993", "1"}, []string{rows[0].ThreadKey, rows[1].ThreadKey, rows[2].ThreadKey})
	assert.True(t, rows[1].E2EE)
	assert.False(t, rows[0].E2EE)
	assert.Equal(t, int64(95), rows[1].SyncGroup)
	assert.Equal(t, "group", rows[0].Kind)

	assert.Len(t, threadRows(threadsFixture(), 2), 2)
	assert.Empty(t, threadRows(nil, 5))
}

func TestThreadRowsJSONShape(t *testing.T) {
	rows := threadRows(threadsFixture(), 1)
	var buf bytes.Buffer
	require.NoError(t, writeJSON(&buf, rows[0]))
	assert.JSONEq(t, `{"thread_key":"2","name":"crew\tteam\nx","kind":"group","e2ee":false,"last_activity_ms":3000,"sync_group":1}`, buf.String())
}

func TestFormatTextRow(t *testing.T) {
	rows := threadRows(threadsFixture(), 0)
	// Tabs and newlines in a name must not break the row layout.
	assert.Equal(t, "group\tfalse\t1970-01-01T00:00:03Z\tcrew team x\t2", formatTextRow(rows[0]))
	assert.Equal(t, "dm\ttrue\t1970-01-01T00:00:02Z\t(no name)\t9007199254740993", formatTextRow(rows[1]))
}
