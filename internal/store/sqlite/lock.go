package sqlite

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

const (
	// lockTimeout is the longest to wait for another process's migration
	// lock. Opening holds it for milliseconds unless a migration runs.
	lockTimeout = 2 * time.Minute
	// lockPoll is how often to try for the lock while another holds it.
	lockPoll = 50 * time.Millisecond
	// lockNotice is how long to wait before saying so.
	lockNotice = time.Second
)

// errLocked means another open file holds the lock.
var errLocked = errors.New("locked")

// lockFile takes an exclusive lock on the file at path, creating it,
// readable and writable by its owner only, if it doesn't exist, and returns
// the function that releases it. The lock belongs to the open file, so it
// excludes other processes and other opens in this one, and the operating
// system releases it when the process ends, however it ends. lockFile waits
// up to timeout for another holder to let go.
func lockFile(ctx context.Context, path string, timeout time.Duration, log *slog.Logger) (unlock func() error, err error) {
	// security: path is the database's, with a suffix: the operator's, from
	// database.url or data_dir (B5). Only a test helper takes one from the
	// environment.
	f, err := os.OpenFile(filepath.Clean(path), os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // G703: see above
	if err != nil {
		return nil, fmt.Errorf("open the database's migration lock: %w", err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start, noticed := time.Now(), false
	for {
		err := tryLock(f)
		if err == nil {
			return func() error { return errors.Join(unlockFile(f), f.Close()) }, nil
		}
		if !errors.Is(err, errLocked) {
			return nil, errors.Join(fmt.Errorf("lock the database for migrating: %w", err), f.Close())
		}
		if !noticed && time.Since(start) >= lockNotice {
			log.Info("waiting for another TrueUp process to finish migrating the database")
			noticed = true
		}
		select {
		case <-waitCtx.Done():
			_ = f.Close()
			if ctx.Err() != nil {
				return nil, fmt.Errorf("waiting for the database's migration lock: %w", ctx.Err())
			}
			return nil, fmt.Errorf("another TrueUp process has held the database's migration lock for %s: is a migration stuck?", timeout)
		case <-time.After(lockPoll):
		}
	}
}
