// Package state owns fbm's on-disk state directory. Everything in it is
// secret-grade (cookies, messagix session state, the age key), so the
// directory is 0700, files are 0600, and it must never sit inside a git tree.
package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// ErrInsideGitTree is returned by Open when the state directory, or any of its
// ancestors, contains a .git entry.
var ErrInsideGitTree = errors.New("state dir is inside a git work tree")

// Store is a directory of named secret files.
type Store struct {
	Dir string
}

// Open resolves dir (default $XDG_STATE_HOME/fbm, else ~/.local/state/fbm),
// refuses git work trees, creates it 0700 and sets a 0077 process umask so
// every file fbm creates afterwards is owner-only.
func Open(dir string) (*Store, error) {
	if dir == "" {
		var err error
		if dir, err = defaultDir(); err != nil {
			return nil, err
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve state dir %s: %w", dir, err)
	}
	if err := refuseGitTree(abs); err != nil {
		return nil, err
	}
	syscall.Umask(0o077)
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, fmt.Errorf("create state dir %s: %w", abs, err)
	}
	// MkdirAll leaves a pre-existing directory untouched.
	if err := os.Chmod(abs, 0o700); err != nil {
		return nil, fmt.Errorf("chmod state dir %s: %w", abs, err)
	}
	return &Store{Dir: abs}, nil
}

func defaultDir() (string, error) {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "fbm"), nil
	}
	home := os.Getenv("HOME")
	if home == "" {
		var err error
		if home, err = os.UserHomeDir(); err != nil {
			return "", fmt.Errorf("resolve home dir: %w", err)
		}
	}
	return filepath.Join(home, ".local", "state", "fbm"), nil
}

// refuseGitTree walks from path (which may not exist yet) to the filesystem
// root looking for a .git file or directory. Symlinks in the existing part of
// the path are resolved first so a link into a repo cannot hide it.
func refuseGitTree(path string) error {
	p := resolveExisting(path)
	for {
		if _, err := os.Lstat(filepath.Join(p, ".git")); err == nil {
			return fmt.Errorf("%w: %s", ErrInsideGitTree, p)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return nil
		}
		p = parent
	}
}

// resolveExisting evaluates symlinks on the longest existing prefix of path
// and re-appends the not-yet-created remainder.
func resolveExisting(path string) string {
	rest := ""
	p := path
	for {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return path
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}

func (s *Store) path(name string) (string, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) || filepath.Base(name) != name {
		return "", fmt.Errorf("invalid secret name %q", name)
	}
	return filepath.Join(s.Dir, name), nil
}

// WriteSecret atomically replaces name with data at mode 0600: write a temp
// file in the same directory, sync, rename over the target.
func (s *Store) WriteSecret(name string, data []byte) (err error) {
	target, err := s.path(name)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir, "."+name+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp for %s: %w", name, err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()
	if err = tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("chmod temp for %s: %w", name, err)
	}
	if _, err = tmp.Write(data); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", name, err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("close temp for %s: %w", name, err)
	}
	if err = os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("rename into %s: %w", name, err)
	}
	return nil
}

// ReadSecret returns the contents of name. A missing file wraps os.ErrNotExist.
func (s *Store) ReadSecret(name string) ([]byte, error) {
	p, err := s.path(name)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	return data, nil
}

// Remove deletes name; a missing file is not an error.
func (s *Store) Remove(name string) error {
	p, err := s.path(name)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", name, err)
	}
	return nil
}

// Stat reports size and age of name without reading it. ok is false, with zero
// values, when the file does not exist or the name is invalid.
func (s *Store) Stat(name string) (size int64, age time.Duration, ok bool) {
	p, err := s.path(name)
	if err != nil {
		return 0, 0, false
	}
	st, err := os.Stat(p)
	if err != nil {
		return 0, 0, false
	}
	return st.Size(), time.Since(st.ModTime()), true
}
