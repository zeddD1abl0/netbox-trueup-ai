package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/version"
)

type result struct {
	code           int
	stdout, stderr string
}

// runCLI runs the command line with the given environment and stdout. The
// default config file is one that doesn't exist, so the host's own is
// never read.
func runCLI(t *testing.T, stdout io.Writer, env []string, args ...string) result {
	t.Helper()
	var out, errOut bytes.Buffer
	if stdout == nil {
		stdout = &out
	}
	a := &app{
		stdout:            stdout,
		stderr:            &errOut,
		environ:           env,
		defaultConfigFile: filepath.Join(t.TempDir(), "absent.yaml"),
	}
	code := run(t.Context(), a, args)
	return result{code: code, stdout: out.String(), stderr: errOut.String()}
}

// writeFile writes a file readable only by its owner into dir.
func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestExitCodes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		args       []string
		env        []string
		wantCode   int
		wantStdout string // a substring
		wantStderr string // a substring; empty means stderr must be empty
	}{
		{name: "no command prints help", wantCode: exitOK, wantStdout: "Available Commands:"},
		{name: "help", args: []string{"--help"}, wantCode: exitOK, wantStdout: "Usage:"},
		{name: "a group prints its help", args: []string{"config"}, wantCode: exitOK, wantStdout: "netbox-trueup-ai config [command]"},
		{name: "version", args: []string{"version"}, wantCode: exitOK, wantStdout: "netbox-trueup-ai "},
		{
			name: "unknown command", args: []string{"bogus"}, wantCode: exitUsage,
			wantStderr: "Error: unknown command \"bogus\" for \"netbox-trueup-ai\"\nRun 'netbox-trueup-ai --help' for usage.\n",
		},
		{
			name: "misspelt command", args: []string{"confg", "show"}, wantCode: exitUsage,
			wantStderr: `did you mean "config"?`,
		},
		{
			name: "unknown subcommand", args: []string{"config", "bogus"}, wantCode: exitUsage,
			wantStderr: "Run 'netbox-trueup-ai config --help' for usage.",
		},
		{
			name: "unexpected argument", args: []string{"version", "extra"}, wantCode: exitUsage,
			wantStderr: `"netbox-trueup-ai version" takes no arguments, but got "extra"`,
		},
		{name: "unknown flag", args: []string{"version", "--nope"}, wantCode: exitUsage, wantStderr: "unknown flag: --nope"},
		{
			name: "unknown output format", args: []string{"config", "show", "-o", "yaml"}, wantCode: exitUsage,
			wantStderr: `invalid argument "yaml" for "-o, --output" flag: must be text or json`,
		},
		{
			name: "invalid setting", args: []string{"config", "show", "--log-level", "loud"}, wantCode: exitConfig,
			wantStderr: `Error: invalid configuration: log.level (flag --log-level): unknown level "loud"`,
		},
		{
			name: "missing config file", args: []string{"config", "show"}, env: []string{"TRUEUP_CONFIG=/nonexistent/config.yaml"},
			wantCode: exitConfig, wantStderr: "read config file: open /nonexistent/config.yaml",
		},
		{
			name: "version doesn't read the configuration", args: []string{"version"}, env: []string{"TRUEUP_LOG_LEVEL=loud"},
			wantCode: exitOK,
		},
		{name: "the netbox group prints its help", args: []string{"netbox"}, wantCode: exitOK, wantStdout: "netbox-trueup-ai netbox [command]"},
		{
			name: "netbox check takes no arguments", args: []string{"netbox", "check", "extra"}, wantCode: exitUsage,
			wantStderr: `"netbox-trueup-ai netbox check" takes no arguments, but got "extra"`,
		},
		{name: "netbox check documents its exit codes", args: []string{"help", "netbox", "check"}, wantCode: exitOK, wantStdout: "4  The token can't read some object types."},
		{name: "help for a command", args: []string{"help", "config", "show"}, wantCode: exitOK, wantStdout: "Print every setting's effective value"},
		{name: "help for an unknown topic", args: []string{"help", "config", "bogus"}, wantCode: exitUsage, wantStderr: `Error: unknown help topic "config bogus"`},
		{name: "completion script", args: []string{"completion", "bash"}, wantCode: exitOK, wantStdout: "bash completion"},
		{name: "completion for an unknown shell", args: []string{"completion", "bogus"}, wantCode: exitUsage, wantStderr: `unknown command "bogus"`},
		{name: "completion with an extra argument", args: []string{"completion", "bash", "extra"}, wantCode: exitUsage, wantStderr: `unknown command "extra"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := runCLI(t, nil, tt.env, tt.args...)
			if r.code != tt.wantCode {
				t.Errorf("exit code = %d, want %d\nstderr: %s", r.code, tt.wantCode, r.stderr)
			}
			if !strings.Contains(r.stdout, tt.wantStdout) {
				t.Errorf("stdout = %q, want it to contain %q", r.stdout, tt.wantStdout)
			}
			if tt.wantStderr == "" && r.stderr != "" {
				t.Errorf("stderr = %q, want it empty", r.stderr)
			}
			if !strings.Contains(r.stderr, tt.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", r.stderr, tt.wantStderr)
			}
		})
	}
}

func TestVersion(t *testing.T) {
	t.Parallel()

	if r := runCLI(t, nil, nil, "version"); r.stdout != "netbox-trueup-ai "+version.String()+"\n" {
		t.Errorf("version printed %q", r.stdout)
	}

	r := runCLI(t, nil, nil, "version", "--output", "json")
	var got struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("version -o json printed %q: %v", r.stdout, err)
	}
	if got.Version != version.String() {
		t.Errorf("version = %q, want %q", got.Version, version.String())
	}
}

// showFixture writes a config file and a token file, and returns the
// arguments and environment that layer flags, environment and file.
func showFixture(t *testing.T) (dir string, args, env []string) {
	t.Helper()
	dir = t.TempDir()
	writeFile(t, dir, "token", "nbt_example.s3cret\n")
	writeFile(t, dir, "config.yaml", `
log:
  level: warn
netbox:
  url: https://netbox.example.com
  token_file: token
sources:
  - name: devicetype-library
    kind: git
    url: https://github.com/netbox-community/devicetype-library.git
    ref: master
    format: devicetype-library
    include: ["device-types/Juniper/**"]
`)
	args = []string{"config", "show", "--config", filepath.Join(dir, "config.yaml"), "--data-dir", "/srv/trueup"}
	env = []string{"TRUEUP_LOG_LEVEL=error", "TRUEUP_DATABASE_URL=postgres://trueup:s3cret@db.example.com/trueup"}
	return dir, args, env
}

func TestConfigShowText(t *testing.T) {
	t.Parallel()

	dir, args, env := showFixture(t)
	r := runCLI(t, nil, env, args...)
	if r.code != exitOK {
		t.Fatalf("exit code = %d\nstderr: %s", r.code, r.stderr)
	}
	want := strings.ReplaceAll(`Config file: {dir}/config.yaml (from --config)

SETTING                   FROM                        VALUE
log.level                 env TRUEUP_LOG_LEVEL        error
log.format                default                     text
data_dir                  flag --data-dir             /srv/trueup
database.url              env TRUEUP_DATABASE_URL     [redacted]
netbox.url                file                        https://netbox.example.com
netbox.token              file, as netbox.token_file  [redacted] (read from {dir}/token)
netbox.ca_bundle          default                     (not set)
netbox.timeout            default                     30s
netbox.max_requests       default                     4
netbox.retries            default                     3
netbox.max_response_size  default                     32MiB
sources[0].name           file                        devicetype-library
sources[0].kind           file                        git
sources[0].url            file                        https://github.com/netbox-community/devicetype-library.git
sources[0].ref            file                        master
sources[0].format         file                        devicetype-library
sources[0].include        file                        ["device-types/Juniper/**"]
sources[0].exclude        file                        []
`, "{dir}", dir)
	if diff := cmp.Diff(want, r.stdout); diff != "" {
		t.Errorf("config show (-want +got):\n%s", diff)
	}
}

func TestConfigShowJSON(t *testing.T) {
	t.Parallel()

	dir, args, env := showFixture(t)
	r := runCLI(t, nil, env, append(args, "-o", "json")...)
	if r.code != exitOK {
		t.Fatalf("exit code = %d\nstderr: %s", r.code, r.stderr)
	}
	var got showJSON
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("config show -o json printed %q: %v", r.stdout, err)
	}
	want := showJSON{
		ConfigFile: &fileJSON{Path: filepath.Join(dir, "config.yaml"), Origin: "flag"},
		Settings: []settingJSON{
			{Key: "log.level", Value: "error", Origin: "env", Name: "TRUEUP_LOG_LEVEL"},
			{Key: "log.format", Value: "text", Origin: "default"},
			{Key: "data_dir", Value: "/srv/trueup", Origin: "flag", Name: "--data-dir"},
			{Key: "database.url", Value: "[redacted]", Secret: true, Origin: "env", Name: "TRUEUP_DATABASE_URL"},
			{Key: "netbox.url", Value: "https://netbox.example.com", Origin: "file", Name: "netbox.url"},
			{
				Key: "netbox.token", Value: "[redacted]", Secret: true, Origin: "file", Name: "netbox.token_file",
				File: filepath.Join(dir, "token"),
			},
			{Key: "netbox.ca_bundle", Value: "", Origin: "default"},
			{Key: "netbox.timeout", Value: "30s", Origin: "default"},
			{Key: "netbox.max_requests", Value: "4", Origin: "default"},
			{Key: "netbox.retries", Value: "3", Origin: "default"},
			{Key: "netbox.max_response_size", Value: "32MiB", Origin: "default"},
			{
				Key: "sources", Origin: "file", Name: "sources",
				Value: []any{map[string]any{
					"name":    "devicetype-library",
					"kind":    "git",
					"url":     "https://github.com/netbox-community/devicetype-library.git",
					"ref":     "master",
					"format":  "devicetype-library",
					"include": []any{"device-types/Juniper/**"},
					"exclude": []any{},
				}},
			},
		},
		Warnings: []warningJSON{},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("config show -o json (-want +got):\n%s", diff)
	}
}

func TestConfigShowWithNoFile(t *testing.T) {
	t.Parallel()

	r := runCLI(t, nil, nil, "config", "show")
	want := `Config file: none

SETTING                   FROM     VALUE
log.level                 default  info
log.format                default  text
data_dir                  default  ` + config.DefaultDataDir() + `
database.url              default  (not set)
netbox.url                default  (not set)
netbox.token              default  (not set)
netbox.ca_bundle          default  (not set)
netbox.timeout            default  30s
netbox.max_requests       default  4
netbox.retries            default  3
netbox.max_response_size  default  32MiB
sources                   default  []
`
	if diff := cmp.Diff(want, r.stdout); diff != "" {
		t.Errorf("config show (-want +got):\n%s", diff)
	}

	r = runCLI(t, nil, nil, "config", "show", "-o", "json")
	var got showJSON
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("config show -o json printed %q: %v", r.stdout, err)
	}
	if got.ConfigFile != nil {
		t.Errorf("config_file = %+v, want null", got.ConfigFile)
	}
}

func TestSecretsNeverReachOutput(t *testing.T) {
	t.Parallel()

	_, args, env := showFixture(t)
	env = append(env, "TRUEUP_LOG_LEVEL=debug")
	for _, extra := range [][]string{nil, {"-o", "json"}, {"--log-format", "json"}} {
		r := runCLI(t, nil, env, append(args, extra...)...)
		if r.code != exitOK {
			t.Fatalf("%v: exit code = %d\nstderr: %s", extra, r.code, r.stderr)
		}
		if strings.Contains(r.stdout+r.stderr, "s3cret") {
			t.Errorf("%v: a secret reached the output:\nstdout: %s\nstderr: %s", extra, r.stdout, r.stderr)
		}
	}
}

func TestWarnings(t *testing.T) {
	t.Parallel()

	const msg = "netbox.url uses http, so the NetBox token crosses the network to netbox.example.com unencrypted: use https"
	env := []string{"TRUEUP_NETBOX_URL=http://netbox.example.com"}

	r := runCLI(t, nil, env, "config", "show")
	if r.code != exitOK {
		t.Fatalf("exit code = %d\nstderr: %s", r.code, r.stderr)
	}
	if want := `level=WARN msg="` + msg + `" setting=netbox.url`; !strings.Contains(r.stderr, want) {
		t.Errorf("stderr = %q, want it to contain %q", r.stderr, want)
	}
	if want := "\nWarnings:\n  " + msg + "\n"; !strings.HasSuffix(r.stdout, want) {
		t.Errorf("stdout = %q, want it to end with %q", r.stdout, want)
	}

	// config show prints warnings whatever the log level.
	r = runCLI(t, nil, append(env, "TRUEUP_LOG_LEVEL=error"), "config", "show", "-o", "json")
	var got showJSON
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("config show -o json printed %q: %v", r.stdout, err)
	}
	if diff := cmp.Diff([]warningJSON{{Key: "netbox.url", Message: msg}}, got.Warnings); diff != "" {
		t.Errorf("warnings (-want +got):\n%s", diff)
	}
}

// An interrupt while the configuration loads is a failure, not a broken
// configuration, which a service manager would stop restarting.
func TestInterruptedLoadIsNotAConfigurationError(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var out, errOut bytes.Buffer
	a := &app{stdout: &out, stderr: &errOut, defaultConfigFile: filepath.Join(t.TempDir(), "absent.yaml")}
	if code := run(ctx, a, []string{"config", "show"}); code != exitFailure {
		t.Errorf("exit code = %d, want %d\nstderr: %s", code, exitFailure, errOut.String())
	}
	if want := "Error: interrupted while loading the configuration: context canceled\n"; errOut.String() != want {
		t.Errorf("stderr = %q, want %q", errOut.String(), want)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestFailureAfterLoadingIsLoggedAsJSON(t *testing.T) {
	t.Parallel()

	r := runCLI(t, failingWriter{}, []string{"TRUEUP_LOG_FORMAT=json"}, "config", "show")
	if r.code != exitFailure {
		t.Errorf("exit code = %d, want %d", r.code, exitFailure)
	}
	var record struct {
		Level    string `json:"level"`
		Msg      string `json:"msg"`
		Error    string `json:"error"`
		ExitCode int    `json:"exit_code"`
	}
	if err := json.Unmarshal([]byte(r.stderr), &record); err != nil {
		t.Fatalf("stderr isn't one JSON record: %q: %v", r.stderr, err)
	}
	if record.Level != "ERROR" || record.Error != "write output: disk full" || record.ExitCode != exitFailure {
		t.Errorf("logged %+v", record)
	}
}
