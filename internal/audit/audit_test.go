package audit

import (
	"context"
	"errors"
	"io/fs"
	"os/user"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

// fakeProcess is a Unix process run by uid, whose user database holds
// users, and whose host is named host.
func fakeProcess(uid int, users map[string]string, host string) process {
	return process{
		goos: "linux",
		uid:  uid,
		lookupID: func(id string) (*user.User, error) {
			name, ok := users[id]
			if !ok {
				return nil, user.UnknownUserIdError(0)
			}
			return &user.User{Uid: id, Username: name}, nil
		},
		windowsAccount: func() (string, string, error) { return "", "", errors.New("not on Windows") },
		hostname: func() (string, error) {
			if host == "" {
				return "", errors.New("no host name")
			}
			return host, nil
		},
	}
}

func env(vars ...string) func(string) string {
	return func(name string) string {
		for i := 0; i+1 < len(vars); i += 2 {
			if vars[i] == name {
				return vars[i+1]
			}
		}
		return ""
	}
}

func TestCommandLineActor(t *testing.T) {
	t.Parallel()
	users := map[string]string{"0": "root", "1000": "carol", "990": "trueup"}
	tests := []struct {
		name string
		p    process
		env  func(string) string
		want Actor
	}{
		{
			"a user the database has",
			fakeProcess(1000, users, "build"), env("USER", "mallory", "LOGNAME", "mallory"),
			Actor{Kind: OSUser, Name: "carol", ID: "1000", Host: "build"},
		},
		{
			"a user the database lacks, as in a container",
			fakeProcess(4242, users, "build"), env("USER", "mallory"),
			Actor{Kind: OSUser, ID: "4242", Host: "build"},
		},
		{
			"a host without a user database, as a container built from scratch",
			withLookup(fakeProcess(1000, users, "build"), &fs.PathError{Op: "open", Path: "/etc/passwd", Err: fs.ErrNotExist}),
			env("USER", "mallory"),
			Actor{Kind: OSUser, ID: "1000", Host: "build"},
		},
		{
			"root through sudo",
			fakeProcess(0, users, "build"), env("SUDO_UID", "1000", "SUDO_USER", "carol"),
			Actor{Kind: OSUser, Name: "root", ID: "0", Host: "build", SudoUser: "carol", SudoUserID: "1000"},
		},
		{
			"a service account through sudo",
			fakeProcess(990, users, "build"), env("SUDO_UID", "1000", "SUDO_USER", "carol"),
			Actor{Kind: OSUser, Name: "trueup", ID: "990", Host: "build", SudoUser: "carol", SudoUserID: "1000"},
		},
		{
			"sudo's user ID without a name",
			fakeProcess(0, users, "build"), env("SUDO_UID", "1000"),
			Actor{Kind: OSUser, Name: "root", ID: "0", Host: "build", SudoUserID: "1000"},
		},
		{
			// sudo to oneself, or SUDO_ variables left in the environment
			// by a shell sudo started, say nothing.
			"sudo's variables naming the user itself",
			fakeProcess(1000, users, "build"), env("SUDO_UID", "1000", "SUDO_USER", "carol"),
			Actor{Kind: OSUser, Name: "carol", ID: "1000", Host: "build"},
		},
		{
			"SUDO_USER without SUDO_UID",
			fakeProcess(1000, users, "build"), env("SUDO_USER", "alice"),
			Actor{Kind: OSUser, Name: "carol", ID: "1000", Host: "build"},
		},
	}
	for _, tt := range tests {
		got, err := commandLineActor(tt.p, tt.env)
		if err != nil {
			t.Errorf("%s: %v", tt.name, err)
		}
		if diff := cmp.Diff(tt.want, got); diff != "" {
			t.Errorf("%s (-want +got):\n%s", tt.name, diff)
		}
	}
}

// withLookup returns p with a user database that fails with err.
func withLookup(p process, err error) process {
	p.lookupID = func(string) (*user.User, error) { return nil, err }
	return p
}

func TestCommandLineActorFails(t *testing.T) {
	t.Parallel()
	users := map[string]string{"1000": "carol"}
	other := fakeProcess(1000, users, "build")
	other.lookupID = func(string) (*user.User, error) { return &user.User{Uid: "1001", Username: "dave"}, nil }
	emptyHost := fakeProcess(1000, users, "build")
	emptyHost.hostname = func() (string, error) { return "", nil }
	tests := []struct {
		name string
		p    process
		want string
	}{
		{"a user database that can't be read", withLookup(fakeProcess(1000, users, "build"), &fs.PathError{Op: "open", Path: "/etc/passwd", Err: fs.ErrPermission}), "look up user ID 1000: open /etc/passwd: permission denied"},
		{"a user database that answers for another user", other, "looking up user ID 1000 found user ID 1001"},
		{"no user ID", fakeProcess(-1, users, "build"), "can't tell which user runs this process on linux"},
		{"a host name that can't be read", fakeProcess(1000, users, ""), "can't tell which host runs this process: no host name"},
		{"an empty host name", emptyHost, "its name is empty"},
	}
	for _, tt := range tests {
		got, err := commandLineActor(tt.p, env())
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: %+v, %v; want an error saying %q", tt.name, got, err, tt.want)
		}
	}
}

func TestCommandLineActorOnWindows(t *testing.T) {
	t.Parallel()
	p := fakeProcess(-1, nil, "WS1")
	p.goos = "windows"
	p.windowsAccount = func() (string, string, error) { return `CORP\carol`, "S-1-5-21-1-2-3-1001", nil }
	want := Actor{Kind: OSUser, Name: `CORP\carol`, ID: "S-1-5-21-1-2-3-1001", Host: "WS1"}
	got, err := commandLineActor(p, env("SUDO_UID", "1000"))
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("on Windows (-want +got):\n%s", diff)
	}
	// An account whose name can't be looked up still has its identifier.
	p.windowsAccount = func() (string, string, error) { return "", "S-1-5-21-1-2-3-1002", nil }
	if got, err := commandLineActor(p, env()); err != nil || got != (Actor{Kind: OSUser, ID: "S-1-5-21-1-2-3-1002", Host: "WS1"}) {
		t.Errorf("on Windows without the account's name = %+v, %v; want its identifier", got, err)
	}
	// Without the token, nobody can be named: an error, not an empty actor.
	p.windowsAccount = func() (string, string, error) { return "", "", errors.New("access denied") }
	if got, err := commandLineActor(p, env()); err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Errorf("on Windows without the token = %+v, %v; want the token's error", got, err)
	}
}

func TestCommandLineActorOfThisProcess(t *testing.T) {
	t.Parallel()
	got, err := CommandLineActor(t.Context(), env("USER", "mallory"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != OSUser || (got.Name == "" && got.ID == "") {
		t.Errorf("CommandLineActor = %+v, want this process's user", got)
	}
	if got.Name == "mallory" {
		t.Error("CommandLineActor took the user's name from $USER")
	}
}

func TestCommandLineActorStopsWaitingWhenCtxEnds(t *testing.T) {
	t.Parallel()
	users := map[string]string{"1000": "carol"}
	// A lookup that blocks, as one asking an unreachable directory does.
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	p := fakeProcess(1000, users, "build")
	p.lookupID = func(id string) (*user.User, error) {
		close(started)
		<-release
		return &user.User{Uid: id, Username: "carol"}, nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	got := make(chan error, 1)
	go func() {
		_, err := lookUpActor(ctx, p, env())
		got <- err
	}()
	<-started
	cancel()
	select {
	case err := <-got:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("lookUpActor after ctx ended = %v, want ctx's error", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("lookUpActor kept waiting for a blocked lookup after ctx ended")
	}

	// A context that has ended looks nothing up.
	var looked atomic.Bool
	q := fakeProcess(1000, users, "build")
	q.lookupID = func(id string) (*user.User, error) {
		looked.Store(true)
		return &user.User{Uid: id, Username: "carol"}, nil
	}
	if _, err := lookUpActor(ctx, q, env()); !errors.Is(err, context.Canceled) {
		t.Errorf("lookUpActor with ctx ended = %v, want ctx's error", err)
	}
	// Give a lookup started anyway the time to show.
	time.Sleep(50 * time.Millisecond)
	if looked.Load() {
		t.Error("lookUpActor looked the user up though ctx had ended")
	}
	// Otherwise it returns the lookup's result.
	if got, err := lookUpActor(t.Context(), q, env()); err != nil || got.Name != "carol" {
		t.Errorf("lookUpActor = %+v, %v; want carol", got, err)
	}
}

func TestParseTime(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.FixedZone("AEST", 10*3600))
	tests := []struct {
		in   string
		want time.Time
	}{
		{"2026-11-23T09:30:00Z", time.Date(2026, 11, 23, 9, 30, 0, 0, time.UTC)},
		{"2026-11-23T19:30:00+10:00", time.Date(2026, 11, 23, 9, 30, 0, 0, time.UTC)},
		{"2026-11-23T09:30:00.123456789Z", time.Date(2026, 11, 23, 9, 30, 0, 123456789, time.UTC)},
		{"90m", time.Date(2026, 11, 23, 0, 30, 0, 0, time.UTC)},
		{"1h30m", time.Date(2026, 11, 23, 0, 30, 0, 0, time.UTC)},
		{"+24h", time.Date(2026, 11, 22, 2, 0, 0, 0, time.UTC)},
		{"7d", time.Date(2026, 11, 16, 2, 0, 0, 0, time.UTC)},
		{"0d", time.Date(2026, 11, 23, 2, 0, 0, 0, time.UTC)},
		{"0", time.Date(2026, 11, 23, 2, 0, 0, 0, time.UTC)},
		{"36500d", time.Date(1926, 12, 18, 2, 0, 0, 0, time.UTC)},
		{"9999-12-31T23:59:59.999999Z", Last},
		{"9999-12-31T23:59:59+10:00", time.Date(9999, 12, 31, 13, 59, 59, 0, time.UTC)},
	}
	for _, tt := range tests {
		got, err := ParseTime(tt.in, now)
		if err != nil || !got.Equal(tt.want) || got.Location() != time.UTC {
			t.Errorf("ParseTime(%q) = %v, %v; want %v in UTC", tt.in, got, err, tt.want)
		}
	}
	for _, in := range []string{
		"", "yesterday", "-5m", "-1d", "+1d", "1.5d", "d", "7D", "1w", "36501d", "213504d", "999999999999d",
		"876001h", "2026-11-23", "2026-11-23 09:30:00", "2026-11-23T09:30:00", " 7d", "4294967295d",
		// The zero time, which a Filter takes for no bound.
		"0001-01-01T00:00:00Z", "0001-01-01T10:00:00+10:00",
		// Past the year 9999 in UTC, or rounded up to it.
		"9999-12-31T23:59:59-10:00", "9999-12-31T23:59:59.9999991Z",
	} {
		if got, err := ParseTime(in, now); err == nil {
			t.Errorf("ParseTime(%q) = %v, want an error", in, got)
		}
	}
	// Each error says what is wrong.
	for _, tt := range []struct{ in, want string }{
		{"yesterday", `"yesterday" is neither an RFC 3339 time`},
		{"-5m", `"-5m" is a duration back from now that is negative, or reaches back more than 100 years`},
		{"-7d", `"-7d" is a duration back from now that is negative`},
		{"36501d", `"36501d" is a duration back from now that is negative`},
		{"999999999999d", `"999999999999d" is a duration back from now that is negative`},
		{"2026-11-31T00:00:00Z", `"2026-11-31T00:00:00Z" isn't an RFC 3339 time, such as 2026-11-23T09:30:00Z: parsing time "2026-11-31T00:00:00Z": day out of range`},
		{"2026-11-23", `"2026-11-23" isn't an RFC 3339 time`},
	} {
		_, err := ParseTime(tt.in, now)
		if err == nil || !strings.HasPrefix(err.Error(), tt.want) {
			t.Errorf("ParseTime(%q)'s error = %v, want it to start %q", tt.in, err, tt.want)
		}
	}
}

func FuzzParseTime(f *testing.F) {
	for _, s := range []string{"7d", "90m", "2026-11-23T09:30:00Z", "-1h", "36500d", "1e3h", "\xff", "9999-12-31T23:59:59-10:00"} {
		f.Add(s)
	}
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	f.Fuzz(func(t *testing.T, s string) {
		got, err := ParseTime(s, now)
		if err != nil {
			return
		}
		if got.Location() != time.UTC {
			t.Errorf("ParseTime(%q) = %v, not in UTC", s, got)
		}
		if got.After(Last) || got.IsZero() {
			t.Errorf("ParseTime(%q) = %v, the zero time or after %v", s, got, Last)
		}
		if _, rfcErr := time.Parse(time.RFC3339Nano, s); rfcErr != nil {
			// A duration: back from now, never further than maxAgo.
			if got.After(now) || got.Before(now.Add(-maxAgo)) {
				t.Errorf("ParseTime(%q) = %v, not within %v before %v", s, got, maxAgo, now)
			}
		}
	})
}

func TestActionWellFormed(t *testing.T) {
	t.Parallel()
	for _, a := range append(Actions(), "netbox.write", "plan.built", "a_b.c_d") {
		if !a.WellFormed() {
			t.Errorf("%q isn't well formed", a)
		}
	}
	for _, a := range []Action{"", "source", "source.", ".added", "Source.added", "source.added.again", "source.add ed", "_x.y", "x._y", "source.added\n"} {
		if a.WellFormed() {
			t.Errorf("%q is well formed", a)
		}
	}
}
