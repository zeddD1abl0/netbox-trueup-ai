package audit

import (
	"errors"
	"os/user"
	"strings"
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
			"no host name",
			fakeProcess(1000, users, ""), env(),
			Actor{Kind: OSUser, Name: "carol", ID: "1000"},
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
	got, err := CommandLineActor(env("USER", "mallory"))
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
	} {
		if got, err := ParseTime(in, now); err == nil {
			t.Errorf("ParseTime(%q) = %v, want an error", in, got)
		}
	}
	_, err := ParseTime("yesterday", now)
	if want := `"yesterday" is neither an RFC 3339 time`; err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("ParseTime's error = %v, want it to start %q", err, want)
	}
}

func FuzzParseTime(f *testing.F) {
	for _, s := range []string{"7d", "90m", "2026-11-23T09:30:00Z", "-1h", "36500d", "1e3h", "\xff"} {
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
