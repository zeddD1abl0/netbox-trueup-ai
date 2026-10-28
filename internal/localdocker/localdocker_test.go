package localdocker

import (
	"net/netip"
	"strings"
	"testing"

	mobynetwork "github.com/moby/moby/api/types/network"
)

func TestCheck(t *testing.T) {
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
		{daemon: "tcp://docker:2375", want: "the Docker daemon is at tcp://docker:2375, but NetBox is published only on 127.0.0.1"},
		{daemon: "tcp://192.0.2.10:2376", want: "needs a Docker daemon on this machine"},
		{daemon: "ssh://user@docker.example.com", want: "needs a Docker daemon on this machine"},
		{daemon: "unix:///var/run/docker.sock", override: "192.0.2.10", overridden: true, want: `TESTCONTAINERS_HOST_OVERRIDE is "192.0.2.10"`},
		{daemon: "unix:///var/run/docker.sock", override: "", overridden: true, want: "TESTCONTAINERS_HOST_OVERRIDE"},
	}
	for _, tt := range tests {
		err := check("NetBox", tt.daemon, tt.override, tt.overridden)
		switch {
		case tt.want == "" && err != nil:
			t.Errorf("check(%q, %q) = %v, want nil", tt.daemon, tt.override, err)
		case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
			t.Errorf("check(%q, %q) = %v, want an error containing %q", tt.daemon, tt.override, err, tt.want)
		}
	}
}

func TestIsLoopback(t *testing.T) {
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
		if got := IsLoopback(host); got != want {
			t.Errorf("IsLoopback(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestBindings(t *testing.T) {
	t.Parallel()
	got := Bindings([]string{"8080/tcp", "5432/tcp"})
	if len(got) != 2 {
		t.Fatalf("Bindings returned %d ports, want 2", len(got))
	}
	for port, bindings := range got {
		if len(bindings) != 1 || bindings[0].HostIP.String() != "127.0.0.1" || bindings[0].HostPort != "" {
			t.Errorf("port %v bindings = %+v, want one binding to 127.0.0.1 on a random port", port, bindings)
		}
	}
}

func TestCheckPorts(t *testing.T) {
	t.Parallel()
	port := mobynetwork.MustParsePort("5432/tcp")
	binding := func(addr string) mobynetwork.PortMap {
		return mobynetwork.PortMap{port: {{HostIP: netip.MustParseAddr(addr), HostPort: "32768"}}}
	}
	for addr, wantErr := range map[string]bool{
		"127.0.0.1":        false,
		"::1":              false,
		"::ffff:127.0.0.1": false,
		"0.0.0.0":          true,
		"::":               true,
		"192.0.2.10":       true,
	} {
		err := checkPorts(binding(addr))
		if (err != nil) != wantErr {
			t.Errorf("checkPorts with a port published on %s = %v, want an error: %v", addr, err, wantErr)
		}
	}
	if err := checkPorts(nil); err != nil {
		t.Errorf("checkPorts with no ports = %v", err)
	}
}
