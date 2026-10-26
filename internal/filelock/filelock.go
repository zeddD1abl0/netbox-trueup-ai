// Package filelock takes locks on files, which the operating system releases
// when the process holding one ends, however it ends: flock on Unix,
// LockFileEx on Windows. A lock belongs to the open file, so it excludes
// other processes and other locks in this one alike.
//
// The locks are advisory: they bind only code that takes them. TrueUp takes
// them to put its own processes in order, such as while it migrates a SQLite
// database or fetches a source.
package filelock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	// poll is how often to try for a lock while another holds it.
	poll = 50 * time.Millisecond
	// notice is how long to wait before calling onWait.
	notice = time.Second
)

// ErrTimeout means another holder kept the lock for longer than the wait
// allowed.
var ErrTimeout = errors.New("another holder kept the lock")

// errLocked means another open file holds a conflicting lock.
var errLocked = errors.New("locked")

// Lock is a lock on a file. Unlock it when done.
type Lock struct {
	f *os.File
}

// Exclusive takes an exclusive lock on the file at path, which no other lock
// may share. See Shared for how it waits.
func Exclusive(ctx context.Context, path string, timeout time.Duration, onWait func()) (*Lock, error) {
	return take(ctx, path, false, timeout, onWait)
}

// Shared takes a shared lock on the file at path, which other shared locks
// may share but an exclusive one may not. It creates the file, readable and
// writable by its owner only, if it doesn't exist. It waits up to timeout for
// a conflicting lock to go, calling onWait, if it isn't nil, once it has
// waited a second, and returns an error wrapping ErrTimeout if the lock
// doesn't go, or the context's error if the context ends first.
func Shared(ctx context.Context, path string, timeout time.Duration, onWait func()) (*Lock, error) {
	return take(ctx, path, true, timeout, onWait)
}

func take(ctx context.Context, path string, shared bool, timeout time.Duration, onWait func()) (*Lock, error) {
	// security: callers name files of their own below the data directory,
	// which the operator configures (B5 in docs/architecture.md).
	f, err := os.OpenFile(filepath.Clean(path), os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // G703: see above
	if err != nil {
		return nil, fmt.Errorf("open the lock file: %w", err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start, noticed := time.Now(), false
	for {
		err := tryLock(f, shared)
		if err == nil {
			return &Lock{f: f}, nil
		}
		if !errors.Is(err, errLocked) {
			return nil, errors.Join(fmt.Errorf("lock %s: %w", filepath.Base(path), err), f.Close())
		}
		if !noticed && onWait != nil && time.Since(start) >= notice {
			onWait()
			noticed = true
		}
		select {
		case <-waitCtx.Done():
			_ = f.Close()
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("%w for %s", ErrTimeout, timeout)
		case <-time.After(poll):
		}
	}
}

// Unlock releases the lock.
func (l *Lock) Unlock() error {
	return errors.Join(unlockFile(l.f), l.f.Close())
}
