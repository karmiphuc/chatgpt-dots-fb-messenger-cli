package main

import (
	"errors"
	"fmt"
	"io"

	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/meta"
	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/state"
)

// lastErrorFile is the checkpoint latch state.RecordFatal writes.
const lastErrorFile = "last_error.json"

// isCheckpointClass reports whether c means Facebook has flagged the account.
// Any automatic retry after one risks turning a checkpoint into a ban, so it
// latches every Meta command until the owner clears it.
func isCheckpointClass(c meta.Class) bool {
	switch c {
	case meta.ClassCheckpoint, meta.ClassChallenge, meta.ClassSuspended, meta.ClassRateLimited:
		return true
	}
	return false
}

// latched reports whether last_error.json blocks Meta commands, with the class
// to print. Only checkpoint classes are ever recorded, so any other record
// latches too, as "unreadable".
func latched(st *state.Store) (last string, ok bool) {
	class, _, ok := st.LastFatal()
	if !ok {
		return "", false
	}
	if !isCheckpointClass(meta.Class(class)) {
		return state.UnreadableFatal, true
	}
	return class, true
}

type ownerMustClear struct {
	ErrorClass string `json:"error_class"`
	Last       string `json:"last"`
}

// enterGate runs before a command contacts Meta: it takes the single-owner
// lock, then checks the checkpoint latch, clearing it first when ownerCleared.
// On success it returns the lock's release func, which the caller defers. On
// failure release is nil and code is the exit code; the reason is printed.
func enterGate(w io.Writer, st *state.Store, cmd string, ownerCleared bool) (release func(), code int) {
	unlock, err := st.Lock()
	if errors.Is(err, state.ErrBusy) {
		return nil, reportClass(w, "busy")
	}
	if err != nil {
		fmt.Fprintf(errOut, "fbm %s: %v\n", cmd, err)
		return nil, 1
	}
	if ownerCleared {
		if err := st.ClearFatal(); err != nil {
			unlock()
			fmt.Fprintf(errOut, "fbm %s: %v\n", cmd, err)
			return nil, 1
		}
	}
	if last, ok := latched(st); ok {
		unlock()
		_ = writeJSON(w, ownerMustClear{ErrorClass: "owner_must_clear", Last: last})
		return nil, 1
	}
	return unlock, 0
}

// recordCheckpoint latches a checkpoint-class result. Callers hold the lock
// and already exit non-zero, so a failed write is a warning on stderr only.
func recordCheckpoint(st *state.Store, cmd string, c meta.Class) {
	if !isCheckpointClass(c) {
		return
	}
	if err := st.RecordFatal(string(c)); err != nil {
		fmt.Fprintf(errOut, "fbm %s: warning: checkpoint not recorded: %v\n", cmd, err)
	}
}
