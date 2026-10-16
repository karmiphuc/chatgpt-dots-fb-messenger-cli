package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix/table"

	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/meta"
)

// writeJSON writes v as one compact line. HTML escaping is off: thread names
// are data for an agent, not markup.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// reportError prints {"error_class": ...} for a failed session open and returns
// the process exit code. Only the class is printed: upstream error text can
// embed redirect URLs.
func reportError(w io.Writer, ce *meta.ClassifiedError) int {
	return reportClass(w, string(ce.Class))
}

func reportClass(w io.Writer, class string) int {
	_ = writeJSON(w, map[string]string{"error_class": class})
	return 1
}

// reportSocket reports a waitSocket result other than "connected" for command
// cmd and returns the exit code. A timeout or the breaker prints its own
// error_class, "socket_timeout" or "disconnected"; an interrupt goes to stderr
// only.
func reportSocket(w io.Writer, cmd, socket string, perr *meta.ClassifiedError) int {
	switch socket {
	case "timeout":
		return reportClass(w, "socket_timeout")
	case "disconnected":
		return reportClass(w, "disconnected")
	case "interrupted":
		fmt.Fprintf(errOut, "fbm %s: interrupted\n", cmd)
		return 1
	}
	if perr == nil {
		perr = &meta.ClassifiedError{Class: meta.ClassUnknown}
	}
	return reportError(w, perr)
}

// threadKind buckets a Messenger thread type for the CLI.
func threadKind(t table.ThreadType) string {
	if t.IsOneToOne() {
		return "dm"
	}
	switch t {
	case table.GROUP_THREAD, table.ENCRYPTED_OVER_WA_GROUP, table.TINCAN_GROUP_DISAPPEARING, table.CARRIER_MESSAGING_GROUP:
		return "group"
	}
	return "other"
}

// threadRow is one line of `fbm threads` JSON output. thread_key is a string:
// thread ids can exceed 2^53, which JSON consumers parse lossily as numbers.
type threadRow struct {
	ThreadKey      string `json:"thread_key"`
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	E2EE           bool   `json:"e2ee"`
	LastActivityMs int64  `json:"last_activity_ms"`
	SyncGroup      int64  `json:"sync_group"`
}

// threadRows turns the initial thread table into rows, newest activity first.
// limit <= 0 means no limit.
func threadRows(tbl *table.LSTable, limit int) []threadRow {
	if tbl == nil {
		return nil
	}
	rows := make([]threadRow, 0, len(tbl.LSDeleteThenInsertThread))
	for _, t := range tbl.LSDeleteThenInsertThread {
		rows = append(rows, threadRow{
			ThreadKey:      strconv.FormatInt(t.ThreadKey, 10),
			Name:           t.ThreadName,
			Kind:           threadKind(t.ThreadType),
			E2EE:           t.ThreadType.IsWhatsApp(),
			LastActivityMs: t.LastActivityTimestampMs,
			SyncGroup:      t.SyncGroup,
		})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].LastActivityMs > rows[j].LastActivityMs })
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows
}

var textFieldCleaner = strings.NewReplacer("\t", " ", "\n", " ", "\r", " ")

// formatTextRow is `fbm threads --format text`: kind, e2ee, last activity
// (RFC 3339 UTC), name, thread key, tab-separated.
func formatTextRow(r threadRow) string {
	name := textFieldCleaner.Replace(r.Name)
	if name == "" {
		name = "(no name)"
	}
	return strings.Join([]string{
		r.Kind,
		strconv.FormatBool(r.E2EE),
		time.UnixMilli(r.LastActivityMs).UTC().Format(time.RFC3339),
		name,
		r.ThreadKey,
	}, "\t")
}
