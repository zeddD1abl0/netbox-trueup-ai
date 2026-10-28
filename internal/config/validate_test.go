package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"runtime"
	"testing"
	"time"
)

// testCertPEM returns a self-signed CA certificate, made afresh, in PEM form.
func testCertPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "TrueUp test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestSize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want int64 // 0 for a problem
	}{
		{in: "32MiB", want: 32 << 20},
		{in: "32 MiB", want: 32 << 20},
		{in: "32mib", want: 32 << 20},
		{in: "1048576", want: 1 << 20},
		{in: "1048576B", want: 1 << 20},
		{in: "1024KiB", want: 1 << 20},
		{in: "1GiB", want: 1 << 30},
		{in: "1048575"},
		{in: "2GiB"},
		{in: "99999999999999999999GiB"},
		{in: "32MB"},
		{in: "32  MiB"},
		{in: "-1MiB"},
		{in: "MiB"},
		{in: ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			ld := &loader{}
			got := ld.size(resolved{key: "k", str: tt.in}, 1<<20, 1<<30, -1)
			if tt.want == 0 {
				if got != -1 || len(ld.problems) != 1 {
					t.Errorf("size(%q) = %d with problems %v, want the default and one problem", tt.in, got, ld.problems)
				}
				return
			}
			if got != tt.want || len(ld.problems) != 0 {
				t.Errorf("size(%q) = %d with problems %v, want %d", tt.in, got, ld.problems, tt.want)
			}
		})
	}
}

// The default maximum response size is written as text in the schema.
func TestDefaultMaxResponseSize(t *testing.T) {
	t.Parallel()

	ld := &loader{}
	s := byKey["netbox.max_response_size"].setting
	if got := ld.size(resolved{key: s.key, str: s.def}, minNetBoxMaxResponse, maxNetBoxMaxResponse, 0); got != defaultNetBoxMaxResponse {
		t.Errorf("default %q is %d bytes, want %d", s.def, got, defaultNetBoxMaxResponse)
	}
}

func TestShortDuration(t *testing.T) {
	t.Parallel()

	for d, want := range map[time.Duration]string{
		time.Second: "1s", 10 * time.Minute: "10m", 90 * time.Second: "1m30s", time.Hour: "1h", 90 * time.Minute: "1h30m",
	} {
		if got := shortDuration(d); got != want {
			t.Errorf("shortDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestGitURLProblem(t *testing.T) {
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
			if got := GitURLProblem(tt.url); got != tt.want {
				t.Errorf("GitURLProblem(%q) = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}

func TestSQLitePath(t *testing.T) {
	t.Parallel()

	const absolute = "must give the database file's absolute path, as in sqlite:///var/lib/netbox-trueup-ai/netbox-trueup-ai.db"
	tests := []struct {
		url, want, problem string
		// only, if set, is "windows" or "unix": the case holds only there.
		only string
	}{
		{url: "sqlite:///var/lib/trueup/x.db", want: "/var/lib/trueup/x.db", only: "unix"},
		{url: "sqlite:/var/lib/trueup/x.db", want: "/var/lib/trueup/x.db", only: "unix"},
		{url: "sqlite:///srv/my%20data/x.db", want: "/srv/my data/x.db", only: "unix"},
		{url: "sqlite:///srv/a/../x.db", want: "/srv/x.db", only: "unix"},
		{url: "sqlite:///C:/ProgramData/trueup/x.db", want: `C:\ProgramData\trueup\x.db`, only: "windows"},
		{url: "sqlite:///x.db", problem: absolute, only: "windows"},
		{url: "sqlite:x.db", problem: absolute},
		{url: "sqlite:", problem: absolute},
		{url: "sqlite://", problem: absolute},
		{url: "sqlite:///srv/trueup/", problem: "must name a file, not a directory: " + absolute},
		{url: "sqlite://localhost/srv/x.db", problem: "must not name a host or user: " + absolute},
		{url: "sqlite://trueup@/srv/x.db", problem: "must not name a host or user: " + absolute},
		{url: "sqlite:///srv/x.db?_pragma=foreign_keys(0)", problem: "must not have a query or fragment: TrueUp sets SQLite's options itself"},
		{url: "sqlite:///srv/x.db?", problem: "must not have a query or fragment: TrueUp sets SQLite's options itself"},
		{url: "sqlite:///srv/x.db#x", problem: "must not have a query or fragment: TrueUp sets SQLite's options itself"},
		{url: "sqlite:///srv/x%00.db", problem: "must not contain a NUL byte"},
		{url: "postgres://db.example.com/trueup", problem: "must be a sqlite URL, such as sqlite:///var/lib/netbox-trueup-ai/netbox-trueup-ai.db"},
		{url: "sqlite:///srv/%zz.db", problem: "must be a sqlite URL, such as sqlite:///var/lib/netbox-trueup-ai/netbox-trueup-ai.db"},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			t.Parallel()
			if onWindows := runtime.GOOS == "windows"; tt.only == "windows" && !onWindows || tt.only == "unix" && onWindows {
				t.Skip("for another platform")
			}
			if got, problem := SQLitePath(tt.url); got != tt.want || problem != tt.problem {
				t.Errorf("SQLitePath(%q) = %q, %q; want %q, %q", tt.url, got, problem, tt.want, tt.problem)
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

func TestPostgresURLProblem(t *testing.T) {
	t.Parallel()
	for raw, wantProblem := range map[string]bool{
		"postgres://db.example.com/trueup":                                          false,
		"postgresql://trueup:s3cret@db.example.com:5433/trueup?sslmode=verify-full": false,
		// url.Parse refuses these, but libpq reads them.
		"postgres://trueup:s3cret@db1.example.com:5433,db2.example.com/trueup":  false,
		"postgres://trueup:s3cret@[2001:db8::1]:5432,[2001:db8::2]:5433/trueup": false,
		"postgres://trueup:p%40ss%2Fword@db.example.com/trueup":                 false,
		"postgres:///trueup?host=/var/run/postgresql":                           false,
		// An @ in a parameter's value is a value.
		"postgres://db.example.com/trueup?user=trueup@EXAMPLE.COM&gssencmode=require": false,
		"postgres:///trueup?host=/run/postgresql&sslrootcert=/etc/ssl/ca@corp.pem":    false,
		// An @, /, ? or # in the password, not percent-encoded.
		"postgres://trueup:p@ss@db.example.com/trueup":                  true,
		"postgres://trueup:20/24@db.example.com/trueup":                 true,
		"postgres://trueup:p@ss/word@db.example.com/trueup":             true,
		"postgres://trueup:pa?ss@db.example.com/trueup":                 true,
		"postgres://trueup:pa?ss@db.example.com/trueup?sslmode=require": true,
		"postgres://trueup:pa#ss@db.example.com/trueup":                 true,
		"postgres://db.example.com/my@db":                               true,
	} {
		if got := PostgresURLProblem(raw) != ""; got != wantProblem {
			t.Errorf("PostgresURLProblem(%q) = %q, want a problem: %v", raw, PostgresURLProblem(raw), wantProblem)
		}
		if !IsPostgresURL(raw) {
			t.Errorf("IsPostgresURL(%q) = false", raw)
		}
	}
	for _, raw := range []string{"postgres:trueup", "sqlite:///srv/trueup.db", "POSTGRES://db.example.com/trueup", "mysql://db.example.com/trueup"} {
		if IsPostgresURL(raw) {
			t.Errorf("IsPostgresURL(%q) = true", raw)
		}
	}
}
