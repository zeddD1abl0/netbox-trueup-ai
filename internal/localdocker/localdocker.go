// Package localdocker keeps the containers that tests start on this machine:
// it checks that the Docker daemon runs here, publishes ports on 127.0.0.1
// only, and checks that Docker did so. internal/netboxtest and
// internal/store/postgres/pgtest use it, since their containers have dummy
// credentials that this repository publishes.
package localdocker

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"strings"

	"github.com/moby/moby/api/types/container"
	mobynetwork "github.com/moby/moby/api/types/network"
	"github.com/testcontainers/testcontainers-go"
)

// Check fails unless the Docker daemon runs on this machine, where ports
// published on 127.0.0.1 can be reached. what names what the caller
// publishes, such as "NetBox", for the message.
func Check(what string) error {
	p, err := testcontainers.NewDockerProvider()
	if err != nil {
		return fmt.Errorf("connect to Docker: %w", err)
	}
	defer func() { _ = p.Close() }()
	override, overridden := os.LookupEnv("TESTCONTAINERS_HOST_OVERRIDE")
	return check(what, p.Client().DaemonHost(), override, overridden)
}

// check checks the daemon's address, such as unix:///var/run/docker.sock or
// tcp://127.0.0.1:2375, and the host testcontainers-go would reach ports on.
func check(what, daemon, override string, overridden bool) error {
	why := what + " is published only on 127.0.0.1, so it needs a Docker daemon on this machine"
	u, err := url.Parse(daemon)
	if err != nil {
		return fmt.Errorf("the Docker daemon's address %q: %w", daemon, err)
	}
	switch u.Scheme {
	case "unix", "npipe":
	case "tcp", "http", "https":
		if !IsLoopback(u.Hostname()) {
			return fmt.Errorf("the Docker daemon is at %s, but %s", daemon, why)
		}
	default:
		return fmt.Errorf("the Docker daemon is at %s, but %s", daemon, why)
	}
	if overridden && !IsLoopback(override) {
		return fmt.Errorf("TESTCONTAINERS_HOST_OVERRIDE is %q, but %s", override, why)
	}
	return nil
}

// IsLoopback reports whether host, a name or address, is this machine.
func IsLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(strings.Trim(host, "[]"))
	return err == nil && ip.Unmap().IsLoopback()
}

// Bindings publishes each port, such as "5432/tcp", on a random port of
// 127.0.0.1.
func Bindings(ports []string) mobynetwork.PortMap {
	bindings := mobynetwork.PortMap{}
	for _, p := range ports {
		bindings[mobynetwork.MustParsePort(p)] = []mobynetwork.PortBinding{{HostIP: netip.AddrFrom4([4]byte{127, 0, 0, 1})}}
	}
	return bindings
}

// Inspect inspects a started container and checks that Docker published
// every one of its ports on 127.0.0.1 only, which rests on how
// testcontainers-go merges its own defaults, so that a new version can't
// quietly publish a database on every interface. It returns what it found,
// for the caller's own checks.
func Inspect(ctx context.Context, c testcontainers.Container) (*container.InspectResponse, error) {
	info, err := c.Inspect(ctx)
	if err != nil {
		return nil, fmt.Errorf("inspect the container: %w", err)
	}
	if info.NetworkSettings == nil {
		return nil, errors.New("no network settings reported for the container")
	}
	if err := checkPorts(info.NetworkSettings.Ports); err != nil {
		return nil, err
	}
	return info, nil
}

// checkPorts checks that every port is published on 127.0.0.1 only.
func checkPorts(ports mobynetwork.PortMap) error {
	for port, bindings := range ports {
		for _, b := range bindings {
			if !b.HostIP.Unmap().IsLoopback() {
				return fmt.Errorf("port %s is published on %s, not 127.0.0.1", port, b.HostIP)
			}
		}
	}
	return nil
}
