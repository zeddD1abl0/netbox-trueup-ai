// Package netboxtest runs a disposable NetBox for integration tests, and
// provisions a v2 API token for it.
//
// The stack is defined once, in compose.yaml: PostgreSQL, Valkey and NetBox
// from netbox-docker's image. `make netbox-up` runs the same file with Docker
// Compose for manual testing. Start reads it and runs each service with
// testcontainers-go, on a network of its own, with ports published only on
// 127.0.0.1. See README.md.
//
// netboxtest only ever talks to the NetBox it starts, or to the one
// `make netbox-up` starts. Never point it at any other NetBox.
package netboxtest

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	mobynetwork "github.com/moby/moby/api/types/network"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
)

// Version is the NetBox release that tests run against: the project's target
// (docs/adr/0003). The NetBox image in compose.yaml and NETBOX_VERSION in the
// Makefile must match it. A unit test checks the image.
const Version = "4.7.2"

// NetBox is a running disposable NetBox. Call Stop to remove it.
type NetBox struct {
	// URL is NetBox's base URL on this machine, such as http://127.0.0.1:32768.
	URL string
	// Token is a write-enabled v2 API token for NetBox's superuser, in the form
	// nbt_<key>.<token>.
	Token config.Secret

	network  *testcontainers.DockerNetwork
	services []startedService // in start order
}

type startedService struct {
	name      string
	container *testcontainers.DockerContainer
}

// Start starts a fresh NetBox and returns once it answers API requests, with a
// token provisioned. A fresh database needs about 4 minutes of migrations, so
// start one NetBox per test binary, in TestMain, and share it.
//
// It needs a Docker daemon on this machine, since ports are published only
// on 127.0.0.1. If Start fails, it removes whatever it started.
func Start(ctx context.Context) (*NetBox, error) {
	return start(ctx, composeFile)
}

// start runs the stack in compose, so that tests can start a broken one.
func start(ctx context.Context, compose []byte) (_ *NetBox, err error) {
	st, order, err := parseStack(compose)
	if err != nil {
		return nil, fmt.Errorf("compose.yaml: %w", err)
	}
	if err := checkLocalDocker(ctx); err != nil {
		return nil, err
	}

	nw, err := network.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("create a Docker network: %w", err)
	}
	nb := &NetBox{network: nw}
	defer func() {
		if err == nil {
			return
		}
		// ctx may be why Start failed, so clean up with a context of its own.
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		if stopErr := nb.Stop(stopCtx); stopErr != nil {
			err = errors.Join(err, fmt.Errorf("clean up after a failed start: %w", stopErr))
		}
	}()

	for _, name := range order {
		ctr, err := startService(ctx, nw, name, st.Services[name])
		if ctr != nil {
			nb.services = append(nb.services, startedService{name: name, container: ctr})
		}
		if err != nil {
			return nil, err
		}
	}

	web := nb.services[slices.IndexFunc(nb.services, func(s startedService) bool { return s.name == netboxService })]
	port, err := web.container.MappedPort(ctx, st.Services[netboxService].containerPorts()[0])
	if err != nil {
		return nil, fmt.Errorf("find NetBox's port: %w", err)
	}
	nb.URL = "http://" + net.JoinHostPort("127.0.0.1", port.Port())

	env := st.Services[netboxService].Environment
	nb.Token, err = provisionToken(ctx, nb.URL, env["SUPERUSER_NAME"], env["SUPERUSER_PASSWORD"], "netboxtest")
	if err != nil {
		return nil, err
	}
	return nb, nil
}

// Stop removes NetBox's containers, their volumes and the network. If a test
// binary dies before calling it, testcontainers-go's reaper removes them.
func (nb *NetBox) Stop(ctx context.Context) error {
	var errs []error
	for _, s := range slices.Backward(nb.services) {
		if err := s.container.Terminate(ctx); err != nil {
			errs = append(errs, fmt.Errorf("remove %s: %w", s.name, err))
		}
	}
	nb.services = nil
	if nb.network != nil {
		if err := nb.network.Remove(ctx); err != nil {
			errs = append(errs, fmt.Errorf("remove the Docker network: %w", err))
		}
		nb.network = nil
	}
	return errors.Join(errs...)
}

// checkLocalDocker fails early if Docker runs somewhere else, where ports
// published on 127.0.0.1 can't be reached.
func checkLocalDocker(ctx context.Context) error {
	p, err := testcontainers.NewDockerProvider()
	if err != nil {
		return fmt.Errorf("connect to Docker: %w", err)
	}
	defer func() { _ = p.Close() }()
	host, err := p.DaemonHost(ctx)
	if err != nil {
		return fmt.Errorf("find the Docker host: %w", err)
	}
	if ip, err := netip.ParseAddr(host); host != "localhost" && (err != nil || !ip.IsLoopback()) {
		return fmt.Errorf("the Docker daemon is on %s, but netboxtest needs one on this machine: it publishes NetBox only on 127.0.0.1", host)
	}
	return nil
}

// startService runs one service from compose.yaml and waits until its health
// check passes, as `docker compose up --wait` does. It returns the container
// whenever one was created, even with an error, so that it can be removed.
func startService(ctx context.Context, nw *testcontainers.DockerNetwork, name string, s service) (*testcontainers.DockerContainer, error) {
	ports := s.containerPorts()
	h := s.Healthcheck
	opts := []testcontainers.ContainerCustomizer{
		network.WithNetwork([]string{name}, nw),
		testcontainers.WithEnv(s.Environment),
		testcontainers.WithExposedPorts(ports...),
		testcontainers.WithConfigModifier(func(c *container.Config) {
			c.User = s.User
			c.Healthcheck = &container.HealthConfig{
				Test:          h.Test,
				StartPeriod:   time.Duration(h.StartPeriod),
				StartInterval: time.Duration(h.StartInterval),
				Interval:      time.Duration(h.Interval),
				Timeout:       time.Duration(h.Timeout),
				Retries:       h.Retries,
			}
		}),
		testcontainers.WithHostConfigModifier(func(hc *container.HostConfig) {
			hc.PortBindings = loopbackBindings(ports)
		}),
		testcontainers.WithWaitStrategyAndDeadline(h.readiness(),
			wait.ForHealthCheck().WithStartupTimeout(h.readiness()).WithPollInterval(time.Second)),
	}
	if len(s.Command) > 0 {
		opts = append(opts, testcontainers.WithCmd(s.Command...))
	}

	ctr, err := testcontainers.Run(ctx, s.Image, opts...)
	if err != nil {
		return ctr, fmt.Errorf("start %s: %w%s", name, err, logTail(ctx, ctr))
	}
	return ctr, nil
}

// loopbackBindings publishes each port on a random port of 127.0.0.1.
func loopbackBindings(ports []string) mobynetwork.PortMap {
	bindings := mobynetwork.PortMap{}
	for _, p := range ports {
		bindings[mobynetwork.MustParsePort(p)] = []mobynetwork.PortBinding{{HostIP: netip.AddrFrom4([4]byte{127, 0, 0, 1})}}
	}
	return bindings
}

// logTail returns the end of a container's log, to explain why it didn't
// become healthy.
func logTail(ctx context.Context, ctr *testcontainers.DockerContainer) string {
	const keep = 40
	if ctr == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	rc, err := ctr.Logs(ctx)
	if err != nil {
		return fmt.Sprintf("\n(its log can't be read: %v)", err)
	}
	defer func() { _ = rc.Close() }()

	var lines []string
	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
		if len(lines) > keep {
			lines = lines[1:]
		}
	}
	tail := "\nThe end of its log:\n" + strings.Join(lines, "\n")
	if err := sc.Err(); err != nil {
		tail += fmt.Sprintf("\n(reading the rest of its log failed: %v)", err)
	}
	return tail
}
