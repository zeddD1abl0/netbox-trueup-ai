package config

import (
	"context"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/pflag"
)

// input describes a Load call. In args, env and expected values, {dir}
// stands for the directory the files are written to.
type input struct {
	files map[string]string // file name to content, each readable only by its owner
	links map[string]string // symbolic link name to target
	args  []string
	env   []string
	// defaultFile is the name of the file to use as the default config file.
	// Empty means one that doesn't exist.
	defaultFile string
}

func (in input) load(t *testing.T) (cfg *Config, dir string, err error) {
	t.Helper()
	dir = t.TempDir()
	for name, content := range in.files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, target := range in.links {
		if err := os.Symlink(filepath.Join(dir, target), filepath.Join(dir, name)); err != nil {
			t.Skipf("can't make a symbolic link: %v", err) // such as on Windows without the privilege
		}
	}
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	RegisterFlags(fs)
	if err := fs.Parse(expandAll(in.args, dir)); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	defaultFile := filepath.Join(dir, "no-such-default.yaml")
	if in.defaultFile != "" {
		defaultFile = filepath.Join(dir, in.defaultFile)
	}
	cfg, err = Load(t.Context(), Input{Flags: fs, Environ: expandAll(in.env, dir), DefaultConfigFile: defaultFile})
	return cfg, dir, err
}

func expand(s, dir string) string {
	return strings.ReplaceAll(s, "{dir}", dir)
}

func expandAll(ss []string, dir string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = expand(s, dir)
	}
	return out
}

var withConfig = []string{"--config", "{dir}/config.yaml"}

// Fake v2 tokens, named for the layer that gives them.
const (
	fileToken    = "nbt_key.file"    //nolint:gosec // G101: a fake token in a test fixture
	envToken     = "nbt_key.env"     //nolint:gosec // G101: a fake token in a test fixture
	flagToken    = "nbt_key.flag"    //nolint:gosec // G101: a fake token in a test fixture
	exampleToken = "nbt_key.example" //nolint:gosec // G101: a fake token in a test fixture
)

// summary is the part of an Entry the tests compare.
type summary struct {
	Value  any
	Origin Origin
	Name   string
	File   string
}

// summarize returns the entries for the keys in want, with {dir} expanded
// in want.
func summarize(cfg *Config, want map[string]summary, dir string) (got, wantExpanded map[string]summary) {
	got, wantExpanded = map[string]summary{}, map[string]summary{}
	for _, e := range cfg.Entries() {
		if w, ok := want[e.Key]; ok {
			got[e.Key] = summary{Value: e.Value, Origin: e.Origin, Name: e.Name, File: e.File}
			if s, isString := w.Value.(string); isString {
				w.Value = expand(s, dir)
			}
			w.File = expand(w.File, dir)
			wantExpanded[e.Key] = w
		}
	}
	return got, wantExpanded
}

func TestLoadPrecedence(t *testing.T) {
	t.Parallel()

	relDataDir, err := filepath.Abs("relative")
	if err != nil {
		t.Fatal(err)
	}
	lib := Source{
		Name: "devicetype-library", Kind: "git", Format: "devicetype-library",
		URL: "https://github.com/netbox-community/devicetype-library.git", Ref: "master",
		Include: []string{"device-types/Juniper/**"}, Exclude: []string{},
	}

	tests := []struct {
		name string
		in   input
		want map[string]summary
	}{
		{
			name: "defaults",
			want: map[string]summary{
				"log.level":    {Value: "info", Origin: OriginDefault},
				"log.format":   {Value: "text", Origin: OriginDefault},
				"data_dir":     {Value: DefaultDataDir(), Origin: OriginDefault},
				"database.url": {Value: "", Origin: OriginDefault},
				"netbox.url":   {Value: "", Origin: OriginDefault},
				"netbox.token": {Value: "", Origin: OriginDefault},
				"sources":      {Value: []Source{}, Origin: OriginDefault},
			},
		},
		{
			name: "file over defaults",
			in:   input{files: map[string]string{"config.yaml": "log:\n  level: debug\n"}, args: withConfig},
			want: map[string]summary{"log.level": {Value: "debug", Origin: OriginFile, Name: "log.level"}},
		},
		{
			name: "environment over file",
			in: input{
				files: map[string]string{"config.yaml": "log:\n  level: debug\n"}, args: withConfig,
				env: []string{"TRUEUP_LOG_LEVEL=warn"},
			},
			want: map[string]summary{"log.level": {Value: "warn", Origin: OriginEnv, Name: "TRUEUP_LOG_LEVEL"}},
		},
		{
			name: "flag over environment",
			in: input{
				files: map[string]string{"config.yaml": "log:\n  level: debug\n"},
				args:  append([]string{"--log-level", "error"}, withConfig...),
				env:   []string{"TRUEUP_LOG_LEVEL=warn"},
			},
			want: map[string]summary{"log.level": {Value: "error", Origin: OriginFlag, Name: "--log-level"}},
		},
		{
			name: "an empty variable counts as unset",
			in: input{
				files: map[string]string{"config.yaml": "log:\n  level: debug\n"}, args: withConfig,
				env: []string{"TRUEUP_LOG_LEVEL="},
			},
			want: map[string]summary{"log.level": {Value: "debug", Origin: OriginFile, Name: "log.level"}},
		},
		{
			name: "YAML null and empty sections count as unset",
			in:   input{files: map[string]string{"config.yaml": "log:\n  level:\nnetbox: {}\ndatabase:\n"}, args: withConfig},
			want: map[string]summary{"log.level": {Value: "info", Origin: OriginDefault}},
		},
		{
			name: "a file of comments is empty",
			in:   input{files: map[string]string{"config.yaml": "# log:\n#   level: debug\n"}, args: withConfig},
			want: map[string]summary{"log.level": {Value: "info", Origin: OriginDefault}},
		},
		{
			name: "a document marker and comments is empty too",
			in:   input{files: map[string]string{"config.yaml": "---\n# log:\n#   level: debug\n"}, args: withConfig},
			want: map[string]summary{"log.level": {Value: "info", Origin: OriginDefault}},
		},
		{
			name: "relative paths in the file are relative to the file",
			in:   input{files: map[string]string{"sub/config.yaml": "data_dir: data\n"}, args: []string{"--config", "{dir}/sub/config.yaml"}},
			want: map[string]summary{"data_dir": {Value: filepath.Join("{dir}", "sub", "data"), Origin: OriginFile, Name: "data_dir"}},
		},
		{
			name: "relative paths in flags are relative to the working directory",
			in:   input{args: []string{"--data-dir", "relative"}},
			want: map[string]summary{"data_dir": {Value: relDataDir, Origin: OriginFlag, Name: "--data-dir"}},
		},
		{
			name: "sources from the file",
			in: input{files: map[string]string{"config.yaml": `
sources:
  - name: devicetype-library
    kind: git
    url: https://github.com/netbox-community/devicetype-library.git
    ref: master
    format: devicetype-library
    include: ["device-types/Juniper/**"]
`}, args: withConfig},
			want: map[string]summary{"sources": {Value: []Source{lib}, Origin: OriginFile, Name: "sources"}},
		},
		{
			name: "sources from the environment replace the file's",
			in: input{
				files: map[string]string{"config.yaml": "sources:\n  - {name: a, kind: git, url: 'https://example.com/a.git', format: devicetype-library}\n"},
				args:  withConfig,
				env:   []string{`TRUEUP_SOURCES=[{"name":"b","kind":"git","url":"git@example.com:b.git","format":"devicetype-library"}]`},
			},
			want: map[string]summary{"sources": {
				Value: []Source{{
					Name: "b", Kind: "git", URL: "git@example.com:b.git", Format: "devicetype-library",
					Include: []string{}, Exclude: []string{},
				}},
				Origin: OriginEnv, Name: "TRUEUP_SOURCES",
			}},
		},
		{
			name: "values are case-insensitive where it is harmless",
			in:   input{env: []string{"TRUEUP_LOG_LEVEL=DEBUG", "TRUEUP_LOG_FORMAT=JSON"}},
			want: map[string]summary{
				"log.level":  {Value: "DEBUG", Origin: OriginEnv, Name: "TRUEUP_LOG_LEVEL"},
				"log.format": {Value: "JSON", Origin: OriginEnv, Name: "TRUEUP_LOG_FORMAT"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, dir, err := tt.in.load(t)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			got, want := summarize(cfg, tt.want, dir)
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("entries (-want +got):\n%s", diff)
			}
		})
	}
}

func TestLoadSettings(t *testing.T) {
	t.Parallel()

	cfg, _, err := input{env: []string{
		"TRUEUP_LOG_LEVEL=debug",
		"TRUEUP_LOG_FORMAT=json",
		"TRUEUP_NETBOX_URL=https://netbox.example.com",
	}}.load(t)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Settings{
		Log:     Log{Level: -4, Format: LogJSON},
		DataDir: DefaultDataDir(),
		NetBox: NetBox{
			URL:     "https://netbox.example.com",
			Timeout: 30 * time.Second, MaxRequests: 4, Retries: 3, MaxResponseSize: 32 << 20,
		},
		Sources: []Source{},
	}
	if diff := cmp.Diff(want, cfg.Settings, cmp.AllowUnexported(Secret{})); diff != "" {
		t.Errorf("Settings (-want +got):\n%s", diff)
	}
}

// The NetBox client's settings take numbers in the config file as well as
// text, and a CA bundle relative to the file.
func TestLoadNetBoxClientSettings(t *testing.T) {
	t.Parallel()

	ca := string(testCertPEM(t))
	tests := []struct {
		name      string
		in        input
		want      NetBox // without CACerts
		wantCerts int
	}{
		{
			name: "defaults",
			want: NetBox{Timeout: 30 * time.Second, MaxRequests: 4, Retries: 3, MaxResponseSize: 32 << 20},
		},
		{
			name: "numbers in the file",
			in: input{
				files: map[string]string{
					"config.yaml": "netbox:\n  ca_bundle: ca.pem\n  timeout: 2m\n  max_requests: 8\n  retries: 0\n  max_response_size: 1048576\n",
					"ca.pem":      "Two certificates, with text around them\n" + ca + "\n" + ca + "the end\n",
				},
				args: withConfig,
			},
			want:      NetBox{CABundle: "{dir}/ca.pem", Timeout: 2 * time.Minute, MaxRequests: 8, Retries: 0, MaxResponseSize: 1 << 20},
			wantCerts: 2,
		},
		{
			name: "text in the environment and flags",
			in: input{
				files: map[string]string{"ca.pem": ca},
				args:  []string{"--netbox-ca-bundle", "{dir}/ca.pem", "--netbox-retries", "10", "--netbox-max-requests", "32"},
				env:   []string{"TRUEUP_NETBOX_TIMEOUT=1500ms", "TRUEUP_NETBOX_MAX_RESPONSE_SIZE=1 GiB"},
			},
			want:      NetBox{CABundle: "{dir}/ca.pem", Timeout: 1500 * time.Millisecond, MaxRequests: 32, Retries: 10, MaxResponseSize: 1 << 30},
			wantCerts: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, dir, err := tt.in.load(t)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			got := cfg.Settings.NetBox
			if len(got.CACerts) != tt.wantCerts {
				t.Errorf("read %d CA certificates, want %d", len(got.CACerts), tt.wantCerts)
			}
			got.CACerts = nil
			want := tt.want
			want.CABundle = expand(want.CABundle, dir)
			if diff := cmp.Diff(want, got, cmp.AllowUnexported(Secret{})); diff != "" {
				t.Errorf("NetBox settings (-want +got):\n%s", diff)
			}
		})
	}
}

func TestLoadSourceLimits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		in          input
		want        SourceLimits
		wantEntries [4]string // max_file_size, max_files, fetch_timeout, max_fetch_size, as Entries shows them
	}{
		{
			name:        "defaults",
			in:          input{env: []string{`TRUEUP_SOURCES=[{"name":"lib","kind":"git","url":"https://example.com/lib.git","format":"devicetype-library"}]`}},
			want:        SourceLimits{MaxFileSize: 1 << 20, MaxFiles: 50000, FetchTimeout: 15 * time.Minute, MaxFetchSize: 2 << 30},
			wantEntries: [4]string{"", "", "", ""}, // unset, so the defaults apply
		},
		{
			name: "numbers and text in the file",
			in: input{files: map[string]string{"config.yaml": `
sources:
  - name: lib
    kind: git
    url: https://example.com/lib.git
    format: devicetype-library
    max_file_size: 65536
    max_files: 100
    fetch_timeout: 90s
    max_fetch_size: 64 MiB
`}, args: withConfig},
			want:        SourceLimits{MaxFileSize: 64 << 10, MaxFiles: 100, FetchTimeout: 90 * time.Second, MaxFetchSize: 64 << 20},
			wantEntries: [4]string{"65536", "100", "90s", "64 MiB"},
		},
		{
			name:        "text in the environment",
			in:          input{env: []string{`TRUEUP_SOURCES=[{"name":"lib","kind":"git","url":"https://example.com/lib.git","format":"devicetype-library","max_files":"1000000","max_file_size":"64MiB","fetch_timeout":"2h","max_fetch_size":"16MiB"}]`}},
			want:        SourceLimits{MaxFileSize: 64 << 20, MaxFiles: 1000000, FetchTimeout: 2 * time.Hour, MaxFetchSize: 16 << 20},
			wantEntries: [4]string{"64MiB", "1000000", "2h", "16MiB"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, _, err := tt.in.load(t)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if diff := cmp.Diff(tt.want, cfg.Settings.Sources[0].Limits); diff != "" {
				t.Errorf("limits (-want +got):\n%s", diff)
			}
			for _, e := range cfg.Entries() {
				if sources, ok := e.Value.([]Source); ok {
					s := sources[0]
					got := [4]string{s.MaxFileSize, s.MaxFiles, s.FetchTimeout, s.MaxFetchSize}
					if got != tt.wantEntries {
						t.Errorf("Entries shows the limits as %q, want %q", got, tt.wantEntries)
					}
					if s.Limits != (SourceLimits{}) {
						t.Errorf("Entries holds Limits %+v, want none", s.Limits)
					}
				}
			}
		})
	}
}

func TestLoadSecrets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		in        input
		wantToken string
		want      summary
	}{
		{
			name: "unset",
			want: summary{Value: "", Origin: OriginDefault},
		},
		{
			name:      "value in the file",
			in:        input{files: map[string]string{"config.yaml": "netbox:\n  token: " + fileToken + "\n"}, args: withConfig},
			wantToken: fileToken,
			want:      summary{Value: redacted, Origin: OriginFile, Name: "netbox.token"},
		},
		{
			name: "_file in the file, relative to it, with a trailing newline",
			in: input{
				files: map[string]string{"config.yaml": "netbox:\n  token_file: token\n", "token": fileToken + "\n"},
				args:  withConfig,
			},
			wantToken: fileToken,
			want:      summary{Value: redacted, Origin: OriginFile, Name: "netbox.token_file", File: "{dir}/token"},
		},
		{
			name:      "a Windows line ending is trimmed too",
			in:        input{files: map[string]string{"token": fileToken + "\r\n"}, env: []string{"TRUEUP_NETBOX_TOKEN_FILE={dir}/token"}},
			wantToken: fileToken,
			want:      summary{Value: redacted, Origin: OriginEnv, Name: "TRUEUP_NETBOX_TOKEN_FILE", File: "{dir}/token"},
		},
		{
			name: "_file in the environment over a value in the file",
			in: input{
				files: map[string]string{"config.yaml": "netbox:\n  token: " + fileToken + "\n", "token": envToken},
				args:  withConfig,
				env:   []string{"TRUEUP_NETBOX_TOKEN_FILE={dir}/token"},
			},
			wantToken: envToken,
			want:      summary{Value: redacted, Origin: OriginEnv, Name: "TRUEUP_NETBOX_TOKEN_FILE", File: "{dir}/token"},
		},
		{
			name: "a value in the environment over _file in the file, which isn't read",
			in: input{
				files: map[string]string{"config.yaml": "netbox:\n  token_file: missing\n"},
				args:  withConfig,
				env:   []string{"TRUEUP_NETBOX_TOKEN=" + envToken},
			},
			wantToken: envToken,
			want:      summary{Value: redacted, Origin: OriginEnv, Name: "TRUEUP_NETBOX_TOKEN"},
		},
		{
			name: "_file flag over a value in the environment",
			in: input{
				files: map[string]string{"token": flagToken},
				args:  []string{"--netbox-token-file", "{dir}/token"},
				env:   []string{"TRUEUP_NETBOX_TOKEN=" + envToken},
			},
			wantToken: flagToken,
			want:      summary{Value: redacted, Origin: OriginFlag, Name: "--netbox-token-file", File: "{dir}/token"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, dir, err := tt.in.load(t)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := cfg.Settings.NetBox.Token.Reveal(); got != tt.wantToken {
				t.Errorf("token = %q, want %q", got, tt.wantToken)
			}
			got, want := summarize(cfg, map[string]summary{"netbox.token": tt.want}, dir)
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("entry (-want +got):\n%s", diff)
			}
		})
	}
}

func TestLoadDatabaseURLFromFile(t *testing.T) {
	t.Parallel()

	const url = "postgres://trueup:s3cret@db.example.com/trueup?sslmode=verify-full" //nolint:gosec // G101: a fake password in a test fixture
	cfg, _, err := input{
		files: map[string]string{"db-url": url + "\n"},
		env:   []string{"TRUEUP_DATABASE_URL_FILE={dir}/db-url"},
	}.load(t)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Settings.Database.URL.Reveal(); got != url {
		t.Errorf("database URL = %q, want %q", got, url)
	}
}

func TestLoadConfigFileChoice(t *testing.T) {
	t.Parallel()

	files := map[string]string{
		"flag.yaml":    "log:\n  level: error\n",
		"env.yaml":     "log:\n  level: warn\n",
		"default.yaml": "log:\n  level: debug\n",
	}
	tests := []struct {
		name      string
		in        input
		want      File
		wantLevel string
	}{
		{
			name:      "--config first",
			in:        input{files: files, args: []string{"--config", "{dir}/flag.yaml"}, env: []string{"TRUEUP_CONFIG={dir}/env.yaml"}, defaultFile: "default.yaml"},
			want:      File{Path: "{dir}/flag.yaml", Origin: OriginFlag},
			wantLevel: "error",
		},
		{
			name:      "then TRUEUP_CONFIG",
			in:        input{files: files, env: []string{"TRUEUP_CONFIG={dir}/env.yaml"}, defaultFile: "default.yaml"},
			want:      File{Path: "{dir}/env.yaml", Origin: OriginEnv},
			wantLevel: "warn",
		},
		{
			name:      "then the default file",
			in:        input{files: files, env: []string{"TRUEUP_CONFIG="}, defaultFile: "default.yaml"},
			want:      File{Path: "{dir}/default.yaml", Origin: OriginDefault},
			wantLevel: "debug",
		},
		{
			name:      "no file if the default doesn't exist",
			in:        input{files: files},
			want:      File{},
			wantLevel: "info",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, dir, err := tt.in.load(t)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			want := File{Path: expand(tt.want.Path, dir), Origin: tt.want.Origin}
			if diff := cmp.Diff(want, cfg.File); diff != "" {
				t.Errorf("File (-want +got):\n%s", diff)
			}
			got, _ := summarize(cfg, map[string]summary{"log.level": {}}, dir)
			if got["log.level"].Value != tt.wantLevel {
				t.Errorf("log.level = %v, want %s", got["log.level"].Value, tt.wantLevel)
			}
		})
	}
}

const (
	badToken = "contains characters a token can't, such as whitespace, control characters or a byte-order mark"
	cutToken = "starts with nbt_, so it is a v2 token, but isn't in the form nbt_<key>.<token>: check that it was copied whole" //nolint:gosec // G101: an error message, not a credential
)

// badSources breaks every rule a source's values follow.
//
//nolint:gosec // G101: a fake password in a test fixture
const badSources = `
sources:
  - name: Device Types
    kind: svn
    url: https://example.com/repo
    ref: -main
    format: netbox
    include: ["/device-types/**", "device-types/../secrets"]
    exclude: [""]
  - name: lib
    kind: git
    url: http://example.com/lib.git
    format: devicetype-library
  - name: lib
    kind: git
    url: https://user:s3cret@example.com/lib.git
    format: devicetype-library
`

func TestLoadProblems(t *testing.T) {
	t.Parallel()

	// {cert} in a file's content stands for a valid certificate.
	cert := string(testCertPEM(t))
	tests := []struct {
		name string
		in   input
		want []string
		// secret must not appear in the error.
		secret string
	}{
		{
			name: "unknown setting in the file",
			in:   input{files: map[string]string{"config.yaml": "netbox:\n  tokn: x\n"}, args: withConfig},
			want: []string{"netbox.tokn (file {dir}/config.yaml): unknown setting"},
		},
		{
			name: "a value where a section belongs",
			in:   input{files: map[string]string{"config.yaml": "netbox: https://netbox.example.com\n"}, args: withConfig},
			want: []string{"netbox (file {dir}/config.yaml): must be a mapping of settings"},
		},
		{
			name: "a setting of the wrong type",
			in:   input{files: map[string]string{"config.yaml": "log:\n  level: 3\n"}, args: withConfig},
			want: []string{"log.level (file {dir}/config.yaml): must be a string"},
		},
		{
			name:   "a secret and its _file in the same file",
			in:     input{files: map[string]string{"config.yaml": "netbox:\n  token: nbt_s3cret\n  token_file: token\n"}, args: withConfig},
			want:   []string{"file {dir}/config.yaml: netbox.token and netbox.token_file are both set; set only one"},
			secret: "s3cret",
		},
		{
			name:   "a secret and its _file in the environment",
			in:     input{env: []string{"TRUEUP_NETBOX_TOKEN=nbt_s3cret", "TRUEUP_NETBOX_TOKEN_FILE=/run/secrets/netbox-token"}},
			want:   []string{"environment: TRUEUP_NETBOX_TOKEN and TRUEUP_NETBOX_TOKEN_FILE are both set; set only one"},
			secret: "s3cret",
		},
		{
			name: "unknown environment variables",
			in:   input{env: []string{"TRUEUP_NETBOX_TOKN=x", "TRUEUP_LOGLEVEL="}},
			want: []string{"env TRUEUP_LOGLEVEL: unknown setting", "env TRUEUP_NETBOX_TOKN: unknown setting"},
		},
		{
			name: "sources in the environment that aren't JSON",
			in:   input{env: []string{"TRUEUP_SOURCES=[x"}},
			want: []string{"sources (env TRUEUP_SOURCES): must be a JSON list of sources: invalid character 'x' looking for beginning of value"},
		},
		{
			name: "--config names a missing file",
			in:   input{args: []string{"--config", "{dir}/missing.yaml"}},
			want: []string{"read config file: open {dir}/missing.yaml: no such file or directory"},
		},
		{
			name: "TRUEUP_CONFIG names a missing file",
			in:   input{env: []string{"TRUEUP_CONFIG={dir}/missing.yaml"}},
			want: []string{"read config file: open {dir}/missing.yaml: no such file or directory"},
		},
		{
			name:   "a config file that isn't a mapping, such as a token",
			in:     input{files: map[string]string{"config.yaml": "nbt_example.s3cret\n"}, args: withConfig},
			want:   []string{"file {dir}/config.yaml: must be a YAML mapping of settings"},
			secret: "nbt_",
		},
		{
			name: "a config file that isn't YAML",
			in:   input{files: map[string]string{"config.yaml": "log: [\n"}, args: withConfig},
			want: []string{"file {dir}/config.yaml: yaml: line 1: did not find expected node content"},
		},
		{
			name: "a duplicate key",
			in:   input{files: map[string]string{"config.yaml": "log:\n  level: debug\n  level: info\n"}, args: withConfig},
			want: []string{`file {dir}/config.yaml: line 3: key "level" is already set at line 2`},
		},
		{
			name:   "a tagged value, which YAML errors would quote",
			in:     input{files: map[string]string{"config.yaml": "netbox:\n  token: !!int nbt_s3cret\n"}, args: withConfig},
			want:   []string{"file {dir}/config.yaml: line 2: YAML tags, such as !!str, aren't supported"},
			secret: "s3cret",
		},
		{
			name: "anchors and aliases",
			in:   input{files: map[string]string{"config.yaml": "log:\n  level: &level debug\nnetbox:\n  url: *level\n"}, args: withConfig},
			want: []string{
				"file {dir}/config.yaml: line 2: YAML anchors and aliases aren't supported",
				"file {dir}/config.yaml: line 4: YAML anchors and aliases aren't supported",
			},
		},
		{
			name:   "a secret file named as the config file, which YAML reads as an alias",
			in:     input{files: map[string]string{"config.yaml": "*s3cret\n"}, args: withConfig},
			want:   []string{"file {dir}/config.yaml: uses a YAML alias, which settings don't support"},
			secret: "s3cret",
		},
		{
			name: "a key that isn't a string",
			in:   input{files: map[string]string{"config.yaml": "1: x\n? [a, b]\n: y\n"}, args: withConfig},
			want: []string{
				"file {dir}/config.yaml: line 1: keys must be plain strings",
				"file {dir}/config.yaml: line 2: keys must be plain strings",
			},
		},
		{
			name: "a dotted key, which would merge with the nested one",
			in:   input{files: map[string]string{"config.yaml": "netbox:\n  url: https://a.example.com\nnetbox.url: https://b.example.com\n"}, args: withConfig},
			want: []string{`file {dir}/config.yaml: line 3: key "netbox.url" contains a dot: nest it under its section instead`},
		},
		{
			name: "more than one document",
			in:   input{files: map[string]string{"config.yaml": "log:\n  level: debug\n---\nnetbox: x\n"}, args: withConfig},
			want: []string{"file {dir}/config.yaml: must hold a single YAML document"},
		},
		{
			name: "problems in every layer, reported together",
			in:   input{env: []string{"TRUEUP_LOGLEVEL=debug", "TRUEUP_LOG_FORMAT=xml", "TRUEUP_NETBOX_URL=ftp://netbox.example.com"}},
			want: []string{
				"env TRUEUP_LOGLEVEL: unknown setting",
				`log.format (env TRUEUP_LOG_FORMAT): unknown format "xml": use text or json`,
				"netbox.url (env TRUEUP_NETBOX_URL): must be an https or http URL, such as https://netbox.example.com",
			},
		},
		{
			name: "a setting with a problem isn't resolved from a lower layer",
			in: input{
				files: map[string]string{"config.yaml": "netbox:\n  token_file: missing\n"},
				args:  withConfig,
				env:   []string{"TRUEUP_NETBOX_TOKEN=" + envToken, "TRUEUP_NETBOX_TOKEN_FILE=/run/secrets/netbox-token"},
			},
			want: []string{"environment: TRUEUP_NETBOX_TOKEN and TRUEUP_NETBOX_TOKEN_FILE are both set; set only one"},
		},
		{
			name: "source names that Windows can't use for a directory",
			in: input{env: []string{`TRUEUP_SOURCES=[` +
				`{"name":"con","kind":"git","url":"https://example.com/a.git","format":"devicetype-library"},` +
				`{"name":"lpt1.cache","kind":"git","url":"https://example.com/b.git","format":"devicetype-library"},` +
				`{"name":"lib.","kind":"git","url":"https://example.com/c.git","format":"devicetype-library"}]`}},
			want: []string{
				`sources[0].name (env TRUEUP_SOURCES): "con" is reserved for a device on Windows`,
				`sources[1].name (env TRUEUP_SOURCES): "lpt1.cache" is reserved for a device on Windows`,
				`sources[2].name (env TRUEUP_SOURCES): "lib." must be up to 63 lowercase letters, digits, dots, hyphens and underscores, starting and ending with a letter or digit`,
			},
		},
		{
			name: "a config file that is too large",
			in:   input{files: map[string]string{"config.yaml": strings.Repeat("#", maxConfigFileSize+1)}, args: withConfig},
			want: []string{"read config file: {dir}/config.yaml is larger than 1048576 bytes"},
		},
		{
			name: "a missing secret file",
			in:   input{files: map[string]string{"config.yaml": "netbox:\n  token_file: missing\n"}, args: withConfig},
			want: []string{"netbox.token (file {dir}/config.yaml): read secret file: open {dir}/missing: no such file or directory"},
		},
		{
			name: "an empty secret file",
			in:   input{files: map[string]string{"token": "\n"}, env: []string{"TRUEUP_NETBOX_TOKEN_FILE={dir}/token"}},
			want: []string{"netbox.token (env TRUEUP_NETBOX_TOKEN_FILE): secret file {dir}/token is empty"},
		},
		{
			name: "a secret file that is a directory",
			in:   input{files: map[string]string{"sub/x": ""}, env: []string{"TRUEUP_NETBOX_TOKEN_FILE={dir}/sub"}},
			want: []string{"netbox.token (env TRUEUP_NETBOX_TOKEN_FILE): read secret file: {dir}/sub is a directory"},
		},
		{
			name: "a secret file that is too large",
			in:   input{files: map[string]string{"token": strings.Repeat("a", maxSecretFileSize+1)}, env: []string{"TRUEUP_NETBOX_TOKEN_FILE={dir}/token"}},
			want: []string{"netbox.token (env TRUEUP_NETBOX_TOKEN_FILE): read secret file: {dir}/token is larger than 65536 bytes"},
		},
		{
			name: "log settings",
			in:   input{env: []string{"TRUEUP_LOG_LEVEL=loud", "TRUEUP_LOG_FORMAT=xml"}},
			want: []string{
				`log.level (env TRUEUP_LOG_LEVEL): unknown level "loud": use debug, info, warn or error`,
				`log.format (env TRUEUP_LOG_FORMAT): unknown format "xml": use text or json`,
			},
		},
		{
			name:   "a database URL with an unsupported scheme",
			in:     input{env: []string{"TRUEUP_DATABASE_URL=mysql://trueup:s3cret@db.example.com/trueup"}},
			want:   []string{"database.url (env TRUEUP_DATABASE_URL): must be a sqlite, postgres or postgresql URL"},
			secret: "s3cret",
		},
		{
			name:   "a database URL that doesn't parse",
			in:     input{env: []string{"TRUEUP_DATABASE_URL=postgres://trueup:s3cret@db.example.com:port/trueup"}},
			want:   []string{"database.url (env TRUEUP_DATABASE_URL): is not a valid URL; percent-encode any special characters in the password"},
			secret: "s3cret",
		},
		{
			name:   "a v2 token without its secret",
			in:     input{env: []string{"TRUEUP_NETBOX_TOKEN=nbt_keykeykeykey"}},
			want:   []string{"netbox.token (env TRUEUP_NETBOX_TOKEN): " + cutToken},
			secret: "keykeykeykey",
		},
		{
			name:   "a v2 token with an empty secret",
			in:     input{files: map[string]string{"token": "nbt_keykeykeykey.\n"}, env: []string{"TRUEUP_NETBOX_TOKEN_FILE={dir}/token"}}, //nolint:gosec // G101: a fake token in a test fixture
			want:   []string{"netbox.token (env TRUEUP_NETBOX_TOKEN_FILE): " + cutToken},
			secret: "keykeykeykey",
		},
		{
			name: "NetBox client settings out of range",
			in: input{env: []string{
				"TRUEUP_NETBOX_TIMEOUT=0s", "TRUEUP_NETBOX_MAX_REQUESTS=0",
				"TRUEUP_NETBOX_RETRIES=99999999999999999999", "TRUEUP_NETBOX_MAX_RESPONSE_SIZE=512KiB",
			}},
			want: []string{
				"netbox.timeout (env TRUEUP_NETBOX_TIMEOUT): 0s is out of range: use 1s to 10m",
				"netbox.max_requests (env TRUEUP_NETBOX_MAX_REQUESTS): 0 is out of range: use 1 to 32",
				"netbox.retries (env TRUEUP_NETBOX_RETRIES): 99999999999999999999 is out of range: use 0 to 10",
				"netbox.max_response_size (env TRUEUP_NETBOX_MAX_RESPONSE_SIZE): 512KiB is out of range: use 1MiB to 1GiB",
			},
		},
		{
			name: "NetBox client settings that don't parse",
			in: input{files: map[string]string{
				"config.yaml": "netbox:\n  timeout: 30\n  max_requests: four\n  retries: 1.5\n  max_response_size: 32MB\n",
			}, args: withConfig},
			want: []string{
				`netbox.timeout (file {dir}/config.yaml): "30" is not a duration: use a number and a unit, such as 30s or 2m`,
				`netbox.max_requests (file {dir}/config.yaml): "four" is not a whole number`,
				`netbox.retries (file {dir}/config.yaml): "1.5" is not a whole number`,
				`netbox.max_response_size (file {dir}/config.yaml): "32MB" is not a size: use a number of bytes, or one with KiB, MiB or GiB, such as 32MiB`,
			},
		},
		{
			name: "a NetBox client setting of the wrong type",
			in:   input{files: map[string]string{"config.yaml": "netbox:\n  retries: true\n  timeout: [30s]\n"}, args: withConfig},
			want: []string{
				"netbox.retries (file {dir}/config.yaml): must be a string or a number",
				"netbox.timeout (file {dir}/config.yaml): must be a string or a number",
			},
		},
		{
			name: "a CA bundle that is missing",
			in:   input{env: []string{"TRUEUP_NETBOX_CA_BUNDLE={dir}/missing.pem"}},
			want: []string{"netbox.ca_bundle (env TRUEUP_NETBOX_CA_BUNDLE): read file: open {dir}/missing.pem: no such file or directory"},
		},
		{
			name:   "a CA bundle holding a private key",
			in:     input{files: map[string]string{"ca.pem": "{cert}" + string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("keykeykeykey")}))}, env: []string{"TRUEUP_NETBOX_CA_BUNDLE={dir}/ca.pem"}},
			want:   []string{`netbox.ca_bundle (env TRUEUP_NETBOX_CA_BUNDLE): file {dir}/ca.pem holds a "PRIVATE KEY" block: a CA bundle holds only certificates`},
			secret: base64.StdEncoding.EncodeToString([]byte("keykeykeykey")),
		},
		{
			name: "a CA bundle without certificates",
			in:   input{files: map[string]string{"ca.pem": "not a certificate\n"}, env: []string{"TRUEUP_NETBOX_CA_BUNDLE={dir}/ca.pem"}},
			want: []string{"netbox.ca_bundle (env TRUEUP_NETBOX_CA_BUNDLE): file {dir}/ca.pem holds no PEM certificates"},
		},
		{
			name: "a CA bundle cut short",
			in:   input{files: map[string]string{"ca.pem": "{cert}-----BEGIN CERTIFICATE-----\nMIIB\n"}, env: []string{"TRUEUP_NETBOX_CA_BUNDLE={dir}/ca.pem"}},
			want: []string{"netbox.ca_bundle (env TRUEUP_NETBOX_CA_BUNDLE): file {dir}/ca.pem has a PEM block that can't be read, such as one cut short"},
		},
		{
			name: "a CA bundle with a block cut short between good ones",
			in:   input{files: map[string]string{"ca.pem": "{cert}-----BEGIN CERTIFICATE-----\nMIIB\n{cert}"}, env: []string{"TRUEUP_NETBOX_CA_BUNDLE={dir}/ca.pem"}},
			want: []string{"netbox.ca_bundle (env TRUEUP_NETBOX_CA_BUNDLE): file {dir}/ca.pem has a PEM block that can't be read, such as one cut short"},
		},
		{
			name: "a CA bundle with a block that isn't base64, between good ones",
			in:   input{files: map[string]string{"ca.pem": "{cert}-----BEGIN CERTIFICATE-----\n!!!!\n-----END CERTIFICATE-----\n{cert}"}, env: []string{"TRUEUP_NETBOX_CA_BUNDLE={dir}/ca.pem"}},
			want: []string{"netbox.ca_bundle (env TRUEUP_NETBOX_CA_BUNDLE): file {dir}/ca.pem has a PEM block that can't be read, such as one cut short"},
		},
		{
			name: "a CA bundle with a certificate that doesn't parse",
			in:   input{files: map[string]string{"ca.pem": "{cert}" + string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("junk")}))}, env: []string{"TRUEUP_NETBOX_CA_BUNDLE={dir}/ca.pem"}},
			want: []string{"netbox.ca_bundle (env TRUEUP_NETBOX_CA_BUNDLE): certificate 2 in file {dir}/ca.pem can't be read: x509: malformed certificate"},
		},
		{
			name:   "a token with whitespace",
			in:     input{env: []string{"TRUEUP_NETBOX_TOKEN=nbt_ex s3cret"}},
			want:   []string{"netbox.token (env TRUEUP_NETBOX_TOKEN): " + badToken},
			secret: "s3cret",
		},
		{
			name:   "a token file with a byte-order mark",
			in:     input{files: map[string]string{"token": "\ufeffnbt_s3cret\n"}, env: []string{"TRUEUP_NETBOX_TOKEN_FILE={dir}/token"}}, //nolint:gosec // G101: a fake token in a test fixture
			want:   []string{"netbox.token (env TRUEUP_NETBOX_TOKEN_FILE): " + badToken},
			secret: "s3cret",
		},
		{
			name:   "a token with a zero-width space",
			in:     input{env: []string{"TRUEUP_NETBOX_TOKEN=nbt_\u200bs3cret"}},
			want:   []string{"netbox.token (env TRUEUP_NETBOX_TOKEN): " + badToken},
			secret: "s3cret",
		},
		{
			name: "a tag from another tool, which would leave its literal text as the value",
			in:   input{files: map[string]string{"config.yaml": "netbox:\n  token: !env NETBOX_TOKEN\n"}, args: withConfig},
			want: []string{"file {dir}/config.yaml: line 2: YAML tags, such as !!str, aren't supported"},
		},
		{
			name:   "a secret file named as the config file, which YAML reads as an anchor",
			in:     input{files: map[string]string{"config.yaml": "&s3cret\n"}, args: withConfig},
			want:   []string{"file {dir}/config.yaml: must be a YAML mapping of settings"},
			secret: "s3cret",
		},
		{
			name: "sources given as a mapping in the file",
			in:   input{files: map[string]string{"config.yaml": "sources:\n  name: lib\n  kind: git\nnetbox:\n  token:\n    value: x\n"}, args: withConfig},
			want: []string{
				"netbox.token (file {dir}/config.yaml): must be a string, not a mapping",
				"sources (file {dir}/config.yaml): must be a list of sources, not a mapping",
			},
		},
		{
			name: "sources given as a mapping in the environment",
			in:   input{env: []string{`TRUEUP_SOURCES={"name":"lib","kind":"git"}`}},
			want: []string{"sources (env TRUEUP_SOURCES): must be a JSON list of sources"},
		},
		{
			name: "an environment variable in the wrong case",
			in:   input{env: []string{"trueup_log_level=debug", "Trueup_Nonsense=x"}},
			want: []string{
				"env Trueup_Nonsense: unknown setting",
				"env trueup_log_level: unknown setting; names are upper case: TRUEUP_LOG_LEVEL",
			},
		},
		{
			name: "a default config file that is a symbolic link to nowhere",
			in:   input{links: map[string]string{"dangling.yaml": "missing.yaml"}, defaultFile: "dangling.yaml"},
			want: []string{"read config file: open {dir}/dangling.yaml: no such file or directory"},
		},
		{
			name: "sources that aren't a list",
			in:   input{files: map[string]string{"config.yaml": "sources: devicetype-library\n"}, args: withConfig},
			want: []string{"sources (file {dir}/config.yaml): must be a list of sources"},
		},
		{
			name: "a source that isn't a mapping, and fields of the wrong type",
			in:   input{files: map[string]string{"config.yaml": "sources:\n  - devicetype-library\n  - {name: a, refs: main, include: x, exclude: [1]}\n"}, args: withConfig},
			want: []string{
				"sources[0] (file {dir}/config.yaml): must be a mapping of source fields",
				"sources[1].exclude[0] (file {dir}/config.yaml): must be a string",
				"sources[1].include (file {dir}/config.yaml): must be a list of strings",
				"sources[1].refs (file {dir}/config.yaml): unknown field",
			},
		},
		{
			name: "a source with limits out of range or of the wrong type",
			in: input{files: map[string]string{"config.yaml": `
sources:
  - name: a
    kind: git
    url: https://example.com/a.git
    format: devicetype-library
    max_file_size: 128MiB
    max_files: 0
    fetch_timeout: 900
    max_fetch_size: 2GB
  - name: b
    kind: git
    url: https://example.com/b.git
    format: devicetype-library
    fetch_timeout: 5s
    max_fetch_size: 1MiB
`}, args: withConfig},
			want: []string{
				"sources[0].max_file_size (file {dir}/config.yaml): 128MiB is out of range: use 1KiB to 64MiB",
				"sources[0].max_files (file {dir}/config.yaml): 0 is out of range: use 1 to 1000000",
				`sources[0].fetch_timeout (file {dir}/config.yaml): "900" is not a duration: use a number and a unit, such as 30s or 2m`,
				`sources[0].max_fetch_size (file {dir}/config.yaml): "2GB" is not a size: use a number of bytes, or one with KiB, MiB or GiB, such as 32MiB`,
				"sources[1].fetch_timeout (file {dir}/config.yaml): 5s is out of range: use 10s to 2h",
				"sources[1].max_fetch_size (file {dir}/config.yaml): 1MiB is out of range: use 16MiB to 64GiB",
			},
		},
		{
			name: "a source limit of the wrong type",
			in:   input{env: []string{`TRUEUP_SOURCES=[{"name":"a","kind":"git","url":"https://example.com/a.git","format":"devicetype-library","max_files":[1]}]`}},
			want: []string{"sources[0].max_files (env TRUEUP_SOURCES): must be a string or a number"},
		},
		{
			name: "a source missing its required fields",
			in:   input{env: []string{`TRUEUP_SOURCES=[{}]`}},
			want: []string{
				"sources[0].name (env TRUEUP_SOURCES): is required",
				"sources[0].kind (env TRUEUP_SOURCES): is required",
				"sources[0].url (env TRUEUP_SOURCES): is required",
				"sources[0].format (env TRUEUP_SOURCES): is required",
			},
		},
		{
			name: "a source with bad values",
			in:   input{files: map[string]string{"config.yaml": badSources}, args: withConfig},
			want: []string{
				`sources[0].name (file {dir}/config.yaml): "Device Types" must be up to 63 lowercase letters, digits, dots, hyphens and underscores, starting and ending with a letter or digit`,
				`sources[0].kind (file {dir}/config.yaml): unknown kind "svn": use git`,
				`sources[0].ref (file {dir}/config.yaml): "-main" is not a valid Git branch or tag name`,
				`sources[0].format (file {dir}/config.yaml): unknown format "netbox": use devicetype-library`,
				`sources[0].include[0] (file {dir}/config.yaml): must be relative to the top of the source, with no leading /`,
				`sources[0].include[1] (file {dir}/config.yaml): must not contain . or .. as a segment`,
				`sources[0].exclude[0] (file {dir}/config.yaml): must not be empty`,
				`sources[1].url (file {dir}/config.yaml): must use https or ssh`,
				`sources[2].name (file {dir}/config.yaml): "lib" is the name of another source`,
				`sources[2].url (file {dir}/config.yaml): must not contain a user name or password`,
			},
			secret: "s3cret",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			in := tt.in
			in.files = map[string]string{}
			for name, content := range tt.in.files {
				in.files[name] = strings.ReplaceAll(content, "{cert}", cert)
			}
			_, dir, err := in.load(t)
			cfgErr, ok := errors.AsType[*Error](err)
			if !ok {
				t.Fatalf("Load error = %v, want an *Error", err)
			}
			var got []string
			for _, p := range cfgErr.Problems {
				got = append(got, p.String())
			}
			if diff := cmp.Diff(expandAll(tt.want, dir), got); diff != "" {
				t.Errorf("problems (-want +got):\n%s", diff)
			}
			if tt.secret != "" && strings.Contains(err.Error(), tt.secret) {
				t.Errorf("error leaks %q: %v", tt.secret, err)
			}
		})
	}
}

func TestLoadNetBoxURL(t *testing.T) {
	t.Parallel()

	const (
		bad     = "must be an https or http URL, such as https://netbox.example.com"
		user    = "must not contain a user name or password: set netbox.token instead"
		query   = "must not have a query or fragment"
		cleartx = "netbox.url uses http, so the NetBox token crosses the network to netbox.example.com:8080 unencrypted: use https"
	)
	tests := []struct {
		url         string
		wantProblem string
		wantWarning string
	}{
		{url: "https://netbox.example.com"},
		{url: "https://netbox.example.com/netbox/"},
		{url: "http://localhost:8000"},
		{url: "http://127.0.0.1:8000"},
		{url: "http://[::1]:8000"},
		{url: "http://netbox.example.com:8080", wantWarning: cleartx},
		{url: "netbox.example.com", wantProblem: bad},
		{url: "ftp://netbox.example.com", wantProblem: bad},
		{url: "https://", wantProblem: bad},
		{url: "https://admin:s3cret@netbox.example.com", wantProblem: user}, //nolint:gosec // G101: a fake password in a test fixture
		{url: "https://netbox.example.com/?token=x", wantProblem: query},
		{url: "https://netbox.example.com/#top", wantProblem: query},
		{url: "https://netbox.example.com/?", wantProblem: query},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			t.Parallel()
			cfg, _, err := input{env: []string{"TRUEUP_NETBOX_URL=" + tt.url}}.load(t)
			if tt.wantProblem != "" {
				want := "invalid configuration: netbox.url (env TRUEUP_NETBOX_URL): " + tt.wantProblem
				if err == nil || err.Error() != want {
					t.Errorf("Load error = %v, want %q", err, want)
				}
				if err != nil && strings.Contains(err.Error(), "s3cret") {
					t.Errorf("error leaks the password: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			var want []Warning
			if tt.wantWarning != "" {
				want = []Warning{{Key: "netbox.url", Message: tt.wantWarning}}
			}
			if diff := cmp.Diff(want, cfg.Warnings); diff != "" {
				t.Errorf("warnings (-want +got):\n%s", diff)
			}
		})
	}
}

func TestLoadWarnsAboutReadableSecrets(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("mode bits don't describe Windows ACLs")
	}

	tests := []struct {
		name  string
		file  string // config.yaml's content
		modes map[string]os.FileMode
		want  []Warning
	}{
		{
			name:  "a secret file readable by all",
			file:  "netbox:\n  token_file: token\n",
			modes: map[string]os.FileMode{"token": 0o644},
			want:  []Warning{{Key: "netbox.token", Message: "secret file {dir}/token can be read by every user on this host: remove read access for others, with chmod o-r or the mode of a Kubernetes or Docker secret mount"}},
		},
		{
			name:  "a secret file readable by its group",
			file:  "netbox:\n  token_file: token\n",
			modes: map[string]os.FileMode{"token": 0o640},
		},
		{
			name:  "a config file holding a secret, readable by all",
			file:  "netbox:\n  token: nbt_key.example\n",
			modes: map[string]os.FileMode{"config.yaml": 0o644},
			want:  []Warning{{Message: "config file {dir}/config.yaml holds a secret and every user on this host can read it: remove read access for others (chmod o-r), or give the secret through its _file setting"}},
		},
		{
			name:  "a config file holding only _file settings, readable by all",
			file:  "netbox:\n  token_file: token\n",
			modes: map[string]os.FileMode{"config.yaml": 0o644},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for name, content := range map[string]string{"config.yaml": tt.file, "token": exampleToken} {
				p := filepath.Join(dir, name)
				mode, ok := tt.modes[name]
				if !ok {
					mode = 0o600
				}
				if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(p, mode); err != nil { // WriteFile's mode is subject to the umask
					t.Fatal(err)
				}
			}
			cfg, err := Load(t.Context(), Input{Environ: []string{"TRUEUP_CONFIG=" + filepath.Join(dir, "config.yaml")}})
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			var want []Warning
			for _, w := range tt.want {
				want = append(want, Warning{Key: w.Key, Message: expand(w.Message, dir)})
			}
			if diff := cmp.Diff(want, cfg.Warnings); diff != "" {
				t.Errorf("warnings (-want +got):\n%s", diff)
			}
		})
	}
}

func TestLoadStopsWhenCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Load(ctx, Input{}); !errors.Is(err, context.Canceled) {
		t.Errorf("Load error = %v, want %v", err, context.Canceled)
	}
}

func TestEntriesAreACopy(t *testing.T) {
	t.Parallel()

	cfg, _, err := input{env: []string{`TRUEUP_SOURCES=[{"name":"a","kind":"git","url":"https://example.com/a.git","format":"devicetype-library","include":["x"]}]`}}.load(t)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	include := func(entries []Entry) []string {
		for _, e := range entries {
			if sources, ok := e.Value.([]Source); ok {
				return sources[0].Include
			}
		}
		t.Fatal("no sources entry")
		return nil
	}
	include(cfg.Entries())[0] = "changed"
	if got := include(cfg.Entries())[0]; got != "x" {
		t.Errorf("changing one Entries result changed the next: include = %q", got)
	}
	if got := cfg.Settings.Sources[0].Include[0]; got != "x" {
		t.Errorf("changing Entries changed Settings: include = %q", got)
	}
}
