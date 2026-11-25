package audit

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"runtime"
	"strconv"
)

// CommandLineActor returns the operating system user running this process,
// with getenv reading the process's environment.
//
// On Unix the user is found by the process's real user ID, in the system's
// user database, never from $USER or $LOGNAME, which anyone can set. Go's
// user.Current falls back to $USER for an ID the database lacks when built
// without cgo, as TrueUp's releases are, so it isn't used there. An ID the
// database lacks, or one where the host has no user database, as in a
// container built from scratch, is recorded alone. On Windows the account
// comes from the
// process's token, with its security identifier, and its name where the
// identifier can be looked up.
//
// A command run through sudo, as another user than the one who ran sudo,
// records sudo's SUDO_UID and SUDO_USER as SudoUserID and SudoUser. sudo
// sets them; the process's own user could set them too, but they never
// replace the user recorded, and root, or a service account that people
// reach through sudo, can change the database anyway.
//
// It fails where it can't tell who runs the process, or on which host: on
// Windows without the process's token, where the user database can't be
// read, or where the host's name can't be. An action nobody can be named
// for isn't taken. It takes no context: neither the user database's lookup nor
// the host's name can be interrupted, and both are local reads.
func CommandLineActor(getenv func(string) string) (Actor, error) {
	return commandLineActor(thisProcess(), getenv)
}

// process is what CommandLineActor reads of the process, so that tests can
// stand in for it.
type process struct {
	goos     string
	uid      int
	lookupID func(uid string) (*user.User, error)
	// windowsAccount is the account's name and security identifier, from
	// the process's token, on Windows.
	windowsAccount func() (name, sid string, err error)
	hostname       func() (string, error)
}

func thisProcess() process {
	return process{
		goos:           runtime.GOOS,
		uid:            os.Getuid(),
		lookupID:       user.LookupId,
		windowsAccount: tokenAccount,
		hostname:       os.Hostname,
	}
}

func commandLineActor(p process, getenv func(string) string) (Actor, error) {
	a := Actor{Kind: OSUser}
	// User IDs belong to a host, and hosts can share PostgreSQL.
	host, err := p.hostname()
	switch {
	case err != nil:
		return Actor{}, fmt.Errorf("can't tell which host runs this process: %w", err)
	case host == "":
		return Actor{}, errors.New("can't tell which host runs this process: its name is empty")
	}
	a.Host = host
	if p.goos == "windows" {
		// os.Getuid is -1 on Windows.
		name, sid, err := p.windowsAccount()
		if err != nil {
			return Actor{}, fmt.Errorf("can't tell which account runs this process: read its token: %w", err)
		}
		a.Name, a.ID = name, sid
		return a, nil
	}
	if p.uid < 0 {
		return Actor{}, fmt.Errorf("can't tell which user runs this process on %s", p.goos)
	}
	a.ID = strconv.Itoa(p.uid)
	u, err := p.lookupID(a.ID)
	var unknown user.UnknownUserIdError
	switch {
	case errors.As(err, &unknown) || errors.Is(err, fs.ErrNotExist):
		// The ID alone, as the user database has no entry for it.
	case err != nil:
		return Actor{}, fmt.Errorf("can't tell which user runs this process: look up user ID %s: %w", a.ID, err)
	case u.Uid != a.ID:
		return Actor{}, fmt.Errorf("can't tell which user runs this process: looking up user ID %s found user ID %s", a.ID, u.Uid)
	default:
		a.Name = u.Username
	}
	if sudoUID := getenv("SUDO_UID"); sudoUID != "" && sudoUID != a.ID {
		a.SudoUser, a.SudoUserID = getenv("SUDO_USER"), sudoUID
	}
	return a, nil
}
