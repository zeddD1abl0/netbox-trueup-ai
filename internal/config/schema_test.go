package config

import (
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

func TestNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		key, env, flag string
	}{
		{key: "database.url", env: "TRUEUP_DATABASE_URL", flag: "database-url"},
		{key: "netbox.token_file", env: "TRUEUP_NETBOX_TOKEN_FILE", flag: "netbox-token-file"},
		{key: "data_dir", env: "TRUEUP_DATA_DIR", flag: "data-dir"},
		{key: "sources", env: "TRUEUP_SOURCES", flag: "sources"},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			t.Parallel()
			if got := envName(tt.key); got != tt.env {
				t.Errorf("envName(%q) = %q, want %q", tt.key, got, tt.env)
			}
			if got := flagName(tt.key); got != tt.flag {
				t.Errorf("flagName(%q) = %q, want %q", tt.key, got, tt.flag)
			}
		})
	}
}

// ADR 0012: the mapping from keys to variable names could give two settings
// the same name, so every setting must map to a distinct variable.
func TestEveryKeyHasItsOwnEnvironmentVariableAndFlag(t *testing.T) {
	t.Parallel()

	envs := map[string]string{EnvConfigFile: "the config file"}
	flags := map[string]string{FlagConfig: "the config file"}
	for _, s := range settings {
		keys := []string{s.key}
		if s.secret {
			keys = append(keys, s.fileKey())
		}
		for _, key := range keys {
			name := envName(key)
			if other, dup := envs[name]; dup {
				t.Errorf("%s and %s both map to %s", other, key, name)
			}
			envs[name] = key
			if byEnv[name] != key {
				t.Errorf("%s reads %q, want %q", name, byEnv[name], key)
			}
		}
		if s.flag {
			name := flagName(s.flagKey())
			if other, dup := flags[name]; dup {
				t.Errorf("%s and %s both map to --%s", other, s.flagKey(), name)
			}
			flags[name] = s.flagKey()
		}
	}
}

// Each setting's variable must actually set it, and nothing else.
func TestEveryEnvironmentVariableSetsItsSetting(t *testing.T) {
	t.Parallel()

	// A valid value for each setting. Adding a setting without one fails
	// the test.
	samples := map[string]string{
		"log.level":    "debug",
		"log.format":   "json",
		"data_dir":     "/srv/trueup",
		"database.url": "postgres://trueup@db.example.com/trueup",
		"netbox.url":   "https://netbox.example.com",
		"netbox.token": "nbt_example",
		"sources":      `[{"name":"lib","kind":"git","url":"https://example.com/lib.git","format":"devicetype-library"}]`,
	}
	for _, s := range settings {
		t.Run(s.key, func(t *testing.T) {
			t.Parallel()
			sample, ok := samples[s.key]
			if !ok {
				t.Fatalf("no sample value for %s", s.key)
			}
			name := envName(s.key)
			cfg, err := Load(t.Context(), Input{Environ: []string{name + "=" + sample}})
			if err != nil {
				t.Fatalf("Load with %s: %v", name, err)
			}
			for _, e := range cfg.Entries() {
				wantOrigin := OriginDefault
				if e.Key == s.key {
					wantOrigin = OriginEnv
				}
				if e.Origin != wantOrigin {
					t.Errorf("with %s set, %s comes from %v, want %v", name, e.Key, e.Origin, wantOrigin)
				}
			}
		})
	}
}

func TestRegisterFlags(t *testing.T) {
	t.Parallel()

	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	RegisterFlags(fs)

	var names []string
	fs.VisitAll(func(f *pflag.Flag) { names = append(names, f.Name) })
	got := strings.Join(names, " ")
	// pflag lists flags sorted. Secrets have only their _file flag.
	want := "config data-dir database-url-file log-format log-level netbox-token-file netbox-url"
	if got != want {
		t.Errorf("flags = %q, want %q", got, want)
	}
	if f := fs.Lookup("log-level"); f.DefValue != "info" {
		t.Errorf("--log-level default = %q, want %q", f.DefValue, "info")
	}
}

func TestOriginDeclared(t *testing.T) {
	t.Parallel()

	for o, want := range map[Origin]bool{OriginDefault: false, OriginFile: true, OriginEnv: true, OriginFlag: true} {
		if got := o.Declared(); got != want {
			t.Errorf("%v.Declared() = %v, want %v", o, got, want)
		}
	}
}
