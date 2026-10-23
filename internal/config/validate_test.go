package config

import (
	"testing"
)

func TestCheckGitURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		url  string
		want string
	}{
		{url: "https://github.com/netbox-community/devicetype-library.git"},
		{url: "ssh://git@git.example.com:2222/group/repo.git"},
		{url: "git@github.com:netbox-community/devicetype-library.git"},
		{url: "https://oauth2@gitlab.example.com/group/repo.git", want: "must not contain a user name or password"},
		{url: "ssh://-oProxyCommand=evil/repo.git", want: "must not have a host or user name starting with -"},
		{url: "ssh://-oProxyCommand=evil@example.com/repo.git", want: "must not have a host or user name starting with -"},
		{url: "-oProxyCommand=evil@example.com:repo.git", want: "must be an https or ssh URL, such as https://github.com/netbox-community/devicetype-library.git or git@github.com:netbox-community/devicetype-library.git"},
		{url: "git@-oProxyCommand=evil:repo.git", want: "must be an https or ssh URL, such as https://github.com/netbox-community/devicetype-library.git or git@github.com:netbox-community/devicetype-library.git"},
		{url: "https://gitlab.example.com/group/repo.git?private_token=x", want: "must not have a query or fragment, which can carry a token"},
		{url: "https://gitlab.example.com/group/repo.git#x", want: "must not have a query or fragment, which can carry a token"},
		{url: "http://example.com/repo.git", want: "must use https or ssh"},
		{url: "git://example.com/repo.git", want: "must use https or ssh"},
		{url: "file:///srv/git/repo.git", want: "must use https or ssh"},
		{url: "https:///repo.git", want: "must name a host"},
		{url: "https://user:s3cret@example.com/repo.git", want: "must not contain a user name or password"}, //nolint:gosec // G101: a fake password in a test fixture
		{url: "ssh://git:s3cret@example.com/repo.git", want: "must not contain a password"},                 //nolint:gosec // G101: a fake password in a test fixture
		{url: "https://example.com:port/repo.git", want: "is not a valid URL"},
		{url: "/srv/git/repo.git", want: "must be an https or ssh URL, such as https://github.com/netbox-community/devicetype-library.git or git@github.com:netbox-community/devicetype-library.git"},
		{url: `C:\repo`, want: "must be an https or ssh URL, such as https://github.com/netbox-community/devicetype-library.git or git@github.com:netbox-community/devicetype-library.git"},
		{url: "example.com:repo.git", want: "must be an https or ssh URL, such as https://github.com/netbox-community/devicetype-library.git or git@github.com:netbox-community/devicetype-library.git"},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			t.Parallel()
			if got := checkGitURL(tt.url); got != tt.want {
				t.Errorf("checkGitURL(%q) = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}

func TestValidRef(t *testing.T) {
	t.Parallel()

	tests := []struct {
		ref  string
		want bool
	}{
		{ref: "master", want: true},
		{ref: "release/4.7", want: true},
		{ref: "v1.2.3", want: true},
		{ref: "refs/heads/main", want: true},
		{ref: "-main"},
		{ref: "/main"},
		{ref: "main/"},
		{ref: ".hidden"},
		{ref: "a/.hidden"},
		{ref: "main."},
		{ref: "main.lock"},
		{ref: "a..b"},
		{ref: "a//b"},
		{ref: "a@{1}"},
		{ref: "@"},
		{ref: "has space"},
		{ref: "tab\tin"},
		{ref: "a~1"},
		{ref: "a^"},
		{ref: "a:b"},
		{ref: "a?"},
		{ref: "a*"},
		{ref: "a[b"},
		{ref: `a\b`},
	}
	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			t.Parallel()
			if got := validRef(tt.ref); got != tt.want {
				t.Errorf("validRef(%q) = %v, want %v", tt.ref, got, tt.want)
			}
		})
	}
}

func TestWindowsDevice(t *testing.T) {
	t.Parallel()

	for name, want := range map[string]bool{
		"con": true, "prn": true, "aux": true, "nul": true, "nul.txt": true, "com1": true, "lpt9.cache": true,
		"console": false, "com": false, "com10": false, "comx": false, "devicetype-library": false,
	} {
		if got := windowsDevice(name); got != want {
			t.Errorf("windowsDevice(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestCheckPattern(t *testing.T) {
	t.Parallel()

	tests := []struct {
		pattern string
		want    string
	}{
		{pattern: "device-types/Juniper/**"},
		{pattern: "device-types/*/*.yaml"},
		{pattern: "a..b/*"},
		{pattern: "", want: "must not be empty"},
		{pattern: "/device-types/**", want: "must be relative to the top of the source, with no leading /"},
		{pattern: "../outside", want: "must not contain .."},
		{pattern: "device-types/../../outside", want: "must not contain .."},
	}
	for _, tt := range tests {
		t.Run(tt.pattern, func(t *testing.T) {
			t.Parallel()
			if got := checkPattern(tt.pattern); got != tt.want {
				t.Errorf("checkPattern(%q) = %q, want %q", tt.pattern, got, tt.want)
			}
		})
	}
}

func TestProblemString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		p    Problem
		want string
	}{
		{p: Problem{Key: "netbox.url", Where: "env TRUEUP_NETBOX_URL", Msg: "is bad"}, want: "netbox.url (env TRUEUP_NETBOX_URL): is bad"},
		{p: Problem{Where: "environment", Msg: "is bad"}, want: "environment: is bad"},
		{p: Problem{Key: "netbox.url", Msg: "is bad"}, want: "netbox.url: is bad"},
		{p: Problem{Msg: "is bad"}, want: "is bad"},
	}
	for _, tt := range tests {
		if got := tt.p.String(); got != tt.want {
			t.Errorf("%#v.String() = %q, want %q", tt.p, got, tt.want)
		}
	}
}

func TestErrorListsEveryProblem(t *testing.T) {
	t.Parallel()

	one := &Error{Problems: []Problem{{Key: "a", Msg: "is bad"}}}
	if got, want := one.Error(), "invalid configuration: a: is bad"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	two := &Error{Problems: []Problem{{Key: "a", Msg: "is bad"}, {Key: "b", Msg: "is worse"}}}
	if got, want := two.Error(), "invalid configuration:\n  a: is bad\n  b: is worse"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}
