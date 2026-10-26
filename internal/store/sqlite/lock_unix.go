//go:build !windows

package sqlite

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// tryLock takes flock's exclusive lock on f without waiting, or returns
// errLocked. flock's locks belong to the open file, unlike the fcntl locks
// SQLite takes on the database itself, with which they don't interfere.
func tryLock(f *os.File) error {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return errLocked
	}
	return err
}

func unlockFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
