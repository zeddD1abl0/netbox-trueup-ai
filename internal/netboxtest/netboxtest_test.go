package netboxtest

import (
	"strings"
	"testing"
)

func TestLocalDocker(t *testing.T) {
	t.Parallel()
	tests := []struct {
		daemon     string
		override   string
		overridden bool
		want       string // in the error; empty for none
	}{
		{daemon: "unix:///var/run/docker.sock"},
		{daemon: "unix:///run/user/1000/docker.sock"},
		{daemon: "npipe:////./pipe/docker_engine"},
		{daemon: "tcp://127.0.0.1:2375"},
		{daemon: "tcp://localhost:2375"},
		{daemon: "tcp://[::1]:2375"},
		{daemon: "unix:///var/run/docker.sock", override: "localhost", overridden: true},
		{daemon: "tcp://docker:2375", want: "the Docker daemon is at tcp://docker:2375"},
		{daemon: "tcp://192.0.2.10:2376", want: "needs a Docker daemon on this machine"},
		{daemon: "ssh://user@docker.example.com", want: "needs a Docker daemon on this machine"},
		{daemon: "unix:///var/run/docker.sock", override: "192.0.2.10", overridden: true, want: `TESTCONTAINERS_HOST_OVERRIDE is "192.0.2.10"`},
		{daemon: "unix:///var/run/docker.sock", override: "", overridden: true, want: "TESTCONTAINERS_HOST_OVERRIDE"},
	}
	for _, tt := range tests {
		err := localDocker(tt.daemon, tt.override, tt.overridden)
		switch {
		case tt.want == "" && err != nil:
			t.Errorf("localDocker(%q, %q) = %v, want nil", tt.daemon, tt.override, err)
		case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
			t.Errorf("localDocker(%q, %q) = %v, want an error containing %q", tt.daemon, tt.override, err, tt.want)
		}
	}
}

func TestIsLoopbackHost(t *testing.T) {
	t.Parallel()
	for host, want := range map[string]bool{
		"localhost":        true,
		"127.0.0.1":        true,
		"127.1.2.3":        true,
		"::1":              true,
		"[::1]":            true,
		"::ffff:127.0.0.1": true,
		"0.0.0.0":          false,
		"192.0.2.10":       false,
		"2001:db8::1":      false,
		"docker":           false,
		"":                 false,
	} {
		if got := isLoopbackHost(host); got != want {
			t.Errorf("isLoopbackHost(%q) = %v, want %v", host, got, want)
		}
	}
}
