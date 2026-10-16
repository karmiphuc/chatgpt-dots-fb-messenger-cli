package state

import (
	"encoding/json"
	"errors"
	"os"
	"time"
)

// lastErrorFile latches the last checkpoint-class result. While it holds one,
// no command contacts Meta until the owner clears it.
const lastErrorFile = "last_error.json"

// UnreadableFatal is the class LastFatal reports for a latch file that exists
// but cannot be read or parsed. Callers treat it as latched.
const UnreadableFatal = "unreadable"

type fatalRecord struct {
	Class string    `json:"class"`
	At    time.Time `json:"at"`
}

// RecordFatal writes class and the current time to last_error.json (0600),
// replacing any earlier record.
func (s *Store) RecordFatal(class string) error {
	raw, err := json.Marshal(fatalRecord{Class: class, At: time.Now().UTC()})
	if err != nil {
		return err
	}
	return s.WriteSecret(lastErrorFile, raw)
}

// LastFatal returns the recorded class and when it was recorded. ok is false
// only when no file exists. A file that cannot be read or parsed, or has no
// class, is reported as UnreadableFatal with ok true, so it fails closed.
func (s *Store) LastFatal() (class string, at time.Time, ok bool) {
	raw, err := s.ReadSecret(lastErrorFile)
	if errors.Is(err, os.ErrNotExist) {
		return "", time.Time{}, false
	}
	if err != nil {
		return UnreadableFatal, time.Time{}, true
	}
	var r fatalRecord
	if err := json.Unmarshal(raw, &r); err != nil || r.Class == "" {
		return UnreadableFatal, time.Time{}, true
	}
	return r.Class, r.At, true
}

// ClearFatal removes last_error.json; a missing file is not an error.
func (s *Store) ClearFatal() error {
	return s.Remove(lastErrorFile)
}
