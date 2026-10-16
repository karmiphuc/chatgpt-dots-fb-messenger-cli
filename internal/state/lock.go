package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lockFile is flocked by every command that contacts Meta, so only one owner
// talks to Facebook at a time.
const lockFile = "fbm.lock"

// ErrBusy is returned by Lock when another process holds the lock.
var ErrBusy = errors.New("another fbm command holds the state lock")

// Lock takes an exclusive, non-blocking flock on fbm.lock and returns the func
// that releases it. The file is never removed: removing it would let two
// processes lock different inodes. Go opens it close-on-exec, so a browser
// started under the lock never inherits it.
func (s *Store) Lock() (unlock func(), err error) {
	f, err := os.OpenFile(filepath.Join(s.Dir, lockFile), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrBusy
		}
		return nil, fmt.Errorf("lock %s: %w", lockFile, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
