package postgres

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
)

// password and user are in the test URLs, and no location or message may
// repeat them.
const (
	password = "s3cret"
	user     = "trueup-user"
)

func TestLocation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, url, want string
	}{
		{"a host", "postgres://trueup-user:s3cret@db.example.com/trueup", "postgres://db.example.com:5432/trueup"},
		{"postgresql, and a port", "postgresql://trueup-user:s3cret@db.example.com:5433/trueup?sslmode=require", "postgres://db.example.com:5433/trueup"},
		// url.Parse refuses these two, which libpq takes.
		{"several hosts", "postgres://trueup-user:s3cret@db1.example.com:5433,db2.example.com/trueup", "postgres://db1.example.com:5433,db2.example.com:5432/trueup"},
		{"several IPv6 addresses", "postgres://trueup-user:s3cret@[2001:db8::1]:5432,[2001:db8::2]:5433/trueup", "postgres://[2001:db8::1]:5432,[2001:db8::2]:5433/trueup"},
		{"an IPv6 address", "postgres://trueup-user:s3cret@[2001:db8::1]/trueup?sslmode=disable", "postgres://[2001:db8::1]:5432/trueup"},
		{"a Unix socket", "postgres:///trueup?host=/var/run/postgresql&user=trueup-user&password=s3cret", "postgres:///trueup?host=%2Fvar%2Frun%2Fpostgresql&port=5432"},
		{"a name to escape", "postgres://trueup-user:s3cret@db.example.com/trueup%20test", "postgres://db.example.com:5432/trueup%20test"},
		{"the password as a parameter", "postgres://db.example.com/trueup?user=trueup-user&password=s3cret&application_name=x", "postgres://db.example.com:5432/trueup"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := parseURL(config.NewSecret(tt.url))
			if err != nil {
				t.Fatalf("parseURL: %v", err)
			}
			got := location(&cfg.ConnConfig.Config)
			if got != tt.want {
				t.Errorf("location = %q, want %q", got, tt.want)
			}
			if strings.Contains(got, password) || strings.Contains(got, user) {
				t.Errorf("location %q names the user or the password", got)
			}
		})
	}
}

func TestParseURLErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, url, want string
	}{
		{"an unknown sslmode", "postgres://trueup-user:s3cret@db.example.com/trueup?sslmode=bogus", "database.url can't be used to connect to PostgreSQL: failed to configure TLS (sslmode is invalid)"},
		{"a connect_timeout that isn't a number", "postgres://trueup-user:s3cret@db.example.com/trueup?connect_timeout=soon", "database.url can't be used to connect to PostgreSQL: invalid connect_timeout (strconv.ParseInt"},
		{"more ports than hosts", "postgres:///trueup?host=a.example.com,b.example.com&port=1,2,3&user=trueup-user&password=s3cret", "database.url can't be used to connect to PostgreSQL: could not match 3 port numbers to 2 hosts"},
		{"an unknown target_session_attrs", "postgres://trueup-user:s3cret@db.example.com/trueup?target_session_attrs=bogus", "database.url can't be used to connect to PostgreSQL: unknown target_session_attrs value: bogus"},
		{"a pool size that isn't a number", "postgres://trueup-user:s3cret@db.example.com/trueup?pool_max_conns=many", "database.url can't be used to connect to PostgreSQL: cannot parse pool_max_conns"},
		{"a port that isn't a number", "postgres://trueup-user:s3cret@db.example.com:port/trueup", "database.url can't be used to connect to PostgreSQL: invalid port"},
		// When the URL itself can't be read, pgx can quote the user name.
		{"an escape that isn't one", "postgres://trueup-user:s3cret@db.example.com/trueup%zz", "database.url can't be used to connect to PostgreSQL: it isn't a URL that libpq would read"},
		{"an escape in the user name", "postgres://trueup-user%zz:s3cret@db.example.com/trueup", "it isn't a URL that libpq would read"},
		{"an escape in the password", "postgres://trueup-user:s3cret%zz@db.example.com/trueup", "it isn't a URL that libpq would read"},
		{"an IPv6 address without its bracket", "postgres://trueup-user:s3cret@[2001:db8::1/trueup", "it isn't a URL that libpq would read"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := parseURL(config.NewSecret(tt.url))
			if _, ok := errors.AsType[*ConfigError](err); !ok || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("parseURL = %v, want a *ConfigError saying %q", err, tt.want)
			}
			if strings.Contains(err.Error(), password) || strings.Contains(err.Error(), user) {
				t.Errorf("the error names the user or the password: %v", err)
			}
		})
	}
}

func TestConnectionDefaults(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, url   string
		wantTimeout time.Duration
		wantApp     string
	}{
		{"TrueUp's defaults", "postgres://db.example.com/trueup", connectTimeout, applicationName},
		{"the URL's", "postgres://db.example.com/trueup?connect_timeout=3&application_name=other", 3 * time.Second, "other"},
		{"no connect timeout", "postgres://db.example.com/trueup?connect_timeout=0", 0, applicationName},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := parseURL(config.NewSecret(tt.url))
			if err != nil {
				t.Fatal(err)
			}
			c := cfg.ConnConfig
			if c.ConnectTimeout != tt.wantTimeout || c.RuntimeParams["application_name"] != tt.wantApp {
				t.Errorf("connect timeout %v and application name %q, want %v and %q",
					c.ConnectTimeout, c.RuntimeParams["application_name"], tt.wantTimeout, tt.wantApp)
			}
			if c.AfterConnect == nil {
				t.Error("connections don't set the search path")
			}
		})
	}
}

func TestConnectTimeoutFromTheEnvironment(t *testing.T) {
	// libpq's variable, which pgx reads, wins over TrueUp's default, 0 too.
	t.Setenv("PGCONNECT_TIMEOUT", "0")
	cfg, err := parseURL(config.NewSecret("postgres://db.example.com/trueup"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConnConfig.ConnectTimeout != 0 {
		t.Errorf("connect timeout %v, want none, as PGCONNECT_TIMEOUT says", cfg.ConnConfig.ConnectTimeout)
	}
}

func TestConnectTimeoutFromAServiceFile(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "pg_service.conf")
	if err := os.WriteFile(file, []byte("[trueup]\nhost=db.example.com\ndbname=trueup\nconnect_timeout=60\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := parseURL(config.NewSecret("postgres:///?service=trueup&servicefile=" + url.QueryEscape(file)))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConnConfig.ConnectTimeout != time.Minute {
		t.Errorf("connect timeout %v, want the service file's minute", cfg.ConnConfig.ConnectTimeout)
	}
}
