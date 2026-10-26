package postgres

import (
	"errors"
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
		{"several hosts", "postgres://trueup-user:s3cret@db1.example.com:5433,db2.example.com/trueup", "postgres://db1.example.com:5433,db2.example.com:5432/trueup"},
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
		{"an unknown sslmode", "postgres://trueup-user:s3cret@db.example.com/trueup?sslmode=bogus", "database.url can't be used to connect to PostgreSQL: sslmode is invalid"},
		{"a connect_timeout that isn't a number", "postgres://trueup-user:s3cret@db.example.com/trueup?connect_timeout=soon", "database.url can't be used to connect to PostgreSQL: strconv.ParseInt"},
		// url.Parse's error repeats the whole URL.
		{"a port that isn't a number", "postgres://trueup-user:s3cret@db.example.com:port/trueup", "database.url can't be used to connect to PostgreSQL: check its host, port and parameters"},
		{"more ports than hosts", "postgres:///trueup?host=a.example.com,b.example.com&port=1,2,3&user=trueup-user&password=s3cret", "database.url can't be used to connect to PostgreSQL: check its host, port and parameters"},
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
