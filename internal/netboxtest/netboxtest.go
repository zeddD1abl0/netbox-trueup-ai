// Package netboxtest runs a disposable NetBox for integration tests, and
// provisions a v2 API token for it.
//
// The stack is defined once, in compose.yaml: PostgreSQL, Valkey and NetBox
// from netbox-docker's image, in one of two profiles: "stock", the image as
// it is, or "branching", the image with netbox-branching added and a
// background worker. `make netbox-up` runs the same file with Docker Compose
// for manual testing. Start reads it and runs each service of the chosen
// profile with testcontainers-go, building the branching image first, on a
// network of its own, and publishes the stack's ports only on 127.0.0.1.
// testcontainers-go's reaper container is the exception: see README.md.
//
// netboxtest only ever talks to the NetBox it starts, or to the one
// `make netbox-up` starts. Never point it at any other NetBox.
package netboxtest

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/localdocker"
)

// Version is the NetBox release that tests run against: the project's target
// (docs/adr/0003). The NetBox image in compose.yaml and NETBOX_VERSION in the
// Makefile must match it, which a unit test checks.
const Version = "4.7.2"

// BranchingVersion is the release of netbox-branching that the Branching
// profile installs: the 1.2 line, for NetBox 4.7. branching/requirements.txt
// must pin it, which a unit test checks.
const BranchingVersion = "1.2.4"

// ProfileVariable is the environment variable that chooses the profile Start
// runs: stock, the default, or branching. It isn't named TRUEUP_..., because
// TrueUp rejects TRUEUP_ variables it doesn't know.
const ProfileVariable = "NETBOXTEST_PROFILE"

// NetBox is a running disposable NetBox. Call Stop to remove it.
type NetBox struct {
	// URL is NetBox's base URL on this machine, such as http://127.0.0.1:32768.
	URL string
	// Token is a write-enabled v2 API token for NetBox's superuser, in the form
	// nbt_<key>.<token>.
	Token config.Secret
	// Profile is the variant running: Stock, or Branching, which has
	// netbox-branching and a worker.
	Profile Profile

	network  *testcontainers.DockerNetwork
	services []startedService // in start order
}

type startedService struct {
	name      string
	container *testcontainers.DockerContainer
}

// Start starts a fresh NetBox of the profile NETBOXTEST_PROFILE names, Stock
// by default, and returns once it answers API requests, with a token
// provisioned, and, for Branching, once NetBox counts its worker running. A
// fresh database needs about 4 minutes of migrations, so start one NetBox per
// test binary, in TestMain, and share it. Give ctx at least 25 minutes:
// compose.yaml's health checks allow NetBox up to 13 minutes to become ready,
// after PostgreSQL and Valkey, up to 15m45s in all, and for Branching the
// worker 1m45s more, and NetBox 2 minutes more to count it, 19m30s in all;
// images may need pulling or building first.
//
// It needs a Docker daemon on this machine, since ports are published only
// on 127.0.0.1. If Start fails, it removes whatever it started.
func Start(ctx context.Context) (*NetBox, error) {
	p, err := profileFromEnv(os.LookupEnv)
	if err != nil {
		return nil, err
	}
	return start(ctx, composeFile, buildContexts, p)
}

// profileFromEnv returns the profile NETBOXTEST_PROFILE names, through
// lookup, such as os.LookupEnv; Stock when it is unset or empty.
func profileFromEnv(lookup func(string) (string, bool)) (Profile, error) {
	v, _ := lookup(ProfileVariable)
	if v == "" {
		return Stock, nil
	}
	if p := Profile(v); slices.Contains(profiles, p) {
		return p, nil
	}
	return "", fmt.Errorf("%s=%q isn't a profile netboxtest runs; use one of %s", ProfileVariable, v, profileNames())
}

// start runs the profile's services from compose, building from contexts, so
// that tests can start a broken stack.
func start(ctx context.Context, compose []byte, contexts fs.FS, profile Profile) (_ *NetBox, err error) {
	st, plans, err := parseStack(compose, contexts)
	if err != nil {
		return nil, fmt.Errorf("compose.yaml: %w", err)
	}
	pl, ok := plans[profile]
	if !ok {
		return nil, fmt.Errorf("compose.yaml: no profile %q", profile)
	}
	if err := localdocker.Check("NetBox"); err != nil {
		return nil, err
	}

	nw, err := network.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("create a Docker network: %w", err)
	}
	nb := &NetBox{Profile: profile, network: nw}
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

	for _, name := range pl.order {
		ctr, err := startService(ctx, nw, name, st.Services[name], contexts)
		if ctr != nil {
			nb.services = append(nb.services, startedService{name: name, container: ctr})
		}
		if err != nil {
			return nil, err
		}
	}

	// From here on NetBox is healthy, so an error is explained by its log.
	web := nb.container(pl.web)
	webPort, _ := containerPort(st.Services[pl.web].Ports[0]) // parseStack checked it
	port, err := web.MappedPort(ctx, fmt.Sprintf("%d/tcp", webPort))
	if err != nil {
		return nil, fmt.Errorf("find NetBox's port: %w%s", err, diagnose(ctx, web))
	}
	nb.URL = "http://" + net.JoinHostPort("127.0.0.1", port.Port())

	env := st.Services[pl.web].Environment
	nb.Token, err = provisionToken(ctx, nb.URL, env["SUPERUSER_NAME"], env["SUPERUSER_PASSWORD"], "netboxtest", true)
	if err != nil {
		return nil, fmt.Errorf("%w%s", err, diagnose(ctx, web))
	}
	if worker := nb.container(workerService); worker != nil {
		if err := nb.waitForWorker(ctx, worker, nb.checkWorker); err != nil {
			return nil, err
		}
	}
	return nb, nil
}

// container returns the container of a service that Start started, or nil.
func (nb *NetBox) container(name string) *testcontainers.DockerContainer {
	if i := slices.IndexFunc(nb.services, func(s startedService) bool { return s.name == name }); i >= 0 {
		return nb.services[i].container
	}
	return nil
}

// workerWait is how long Start waits for NetBox to count its worker running,
// once the worker passes its health check, which takes it a few seconds.
const workerWait = 2 * time.Minute

// waitForWorker waits until NetBox counts a background worker running. The
// worker's health check shows only that its process runs; NetBox counts the
// workers that have connected to Valkey to take jobs, such as provisioning a
// branch. It checks with alive each time that the worker still runs, and
// stops at once if it doesn't.
func (nb *NetBox) waitForWorker(ctx context.Context, worker *testcontainers.DockerContainer, alive func(context.Context) error) error {
	waitCtx, cancel := context.WithTimeout(ctx, workerWait)
	defer cancel()
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	// NetBox has just started, so a request that got no answer, or a 5xx one,
	// is worth trying again; the last one's error explains a wait that runs out.
	var lastErr error
	for {
		if err := alive(waitCtx); err != nil && waitCtx.Err() == nil {
			return fmt.Errorf("wait for NetBox to count its worker: %w", err)
		}
		var st struct {
			Workers *int `json:"rq-workers-running"`
		}
		err := nb.request(waitCtx, "", http.MethodGet, "/api/status/", nil, nil, &st, http.StatusOK)
		switch {
		case err != nil:
			// A request the wait's own end cut short says nothing about NetBox.
			if waitCtx.Err() == nil {
				if !mayPass(err) {
					return fmt.Errorf("wait for NetBox to count its worker: %w%s", err, diagnose(ctx, worker))
				}
				lastErr = err
			}
		case st.Workers == nil:
			return fmt.Errorf("NetBox's /api/status/ doesn't say how many workers are running%s", diagnose(ctx, worker))
		case *st.Workers > 0:
			return nil
		default:
			lastErr = nil
		}
		select {
		case <-waitCtx.Done():
			why := fmt.Errorf("within %v", workerWait)
			if ctx.Err() != nil {
				why = context.Cause(ctx)
			}
			if lastErr != nil {
				why = fmt.Errorf("%w; the last request failed: %w", why, lastErr)
			}
			return fmt.Errorf("NetBox counted no running worker: %w%s", why, diagnose(ctx, worker))
		case <-tick.C:
		}
	}
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

// startService runs one service from compose.yaml, building its image from
// contexts first if it has a build, and waits until its health check passes,
// as `docker compose up --wait` does. It returns the container whenever one
// was created, even with an error, so that it can be removed.
func startService(ctx context.Context, nw *testcontainers.DockerNetwork, name string, s service, contexts fs.FS) (*testcontainers.DockerContainer, error) {
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
			hc.PortBindings = localdocker.Bindings(ports)
		}),
		// Runs once the container has started, before the wait below.
		testcontainers.WithAdditionalLifecycleHooks(testcontainers.ContainerLifecycleHooks{
			PostStarts: []testcontainers.ContainerHook{
				func(ctx context.Context, c testcontainers.Container) error {
					return checkStarted(ctx, c, nw.Name, name)
				},
			},
		}),
		testcontainers.WithWaitStrategyAndDeadline(h.readiness(),
			wait.ForHealthCheck().WithStartupTimeout(h.readiness()).WithPollInterval(time.Second)),
	}
	if len(s.Command) > 0 {
		opts = append(opts, testcontainers.WithCmd(s.Command...))
	}
	var buildLog *tailWriter
	if s.Build != nil {
		dir, tag, err := writeContext(ctx, contexts, s.Build.Context, "")
		if err != nil {
			return nil, fmt.Errorf("start %s: %w", name, err)
		}
		defer func() { _ = os.RemoveAll(dir) }()
		buildLog = &tailWriter{keep: 40}
		opts = append(opts, testcontainers.WithDockerfile(testcontainers.FromDockerfile{
			Context:    dir,
			Dockerfile: "Dockerfile",
			Repo:       builtImageRepo + s.Build.Context,
			// Named for its content, so a build reuses Docker's cache, and
			// other services and test binaries reuse the image.
			Tag:            tag,
			KeepImage:      true,
			BuildLogWriter: buildLog,
		}))
	}

	ctr, err := testcontainers.Run(ctx, s.Image, opts...)
	if err != nil {
		if ctr == nil && buildLog != nil {
			// The build may have worked; its log's last line says.
			return nil, fmt.Errorf("start %s: %w\nNo container was created.%s", name, err, buildLog.report())
		}
		return ctr, fmt.Errorf("start %s: %w%s", name, err, diagnose(ctx, ctr))
	}
	return ctr, nil
}

// checkStarted checks that Docker did what netboxtest asked: every port
// published on 127.0.0.1 only, and the container on the stack's network
// under its service name. Both rest on how testcontainers-go merges its own
// defaults, so a new version can't quietly publish a database on every
// interface, or leave NetBox unable to find it.
func checkStarted(ctx context.Context, c testcontainers.Container, networkName, alias string) error {
	info, err := localdocker.Inspect(ctx, c)
	if err != nil {
		return err
	}
	ep, ok := info.NetworkSettings.Networks[networkName]
	if !ok || ep == nil {
		return fmt.Errorf("the container isn't on the stack's network %s", networkName)
	}
	if !slices.Contains(ep.Aliases, alias) {
		return fmt.Errorf("the container is on the stack's network without the name %s", alias)
	}
	return nil
}

// diagnose explains why a container isn't ready: its last health check
// results, which hold the check's own output, and the end of its log.
func diagnose(ctx context.Context, ctr *testcontainers.DockerContainer) string {
	if ctr == nil {
		return ""
	}
	// ctx may have run out, which may be why the container failed.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	return healthReport(ctx, ctr) + logTail(ctx, ctr)
}

// healthReport returns the container's health status and its last few health
// check results.
func healthReport(ctx context.Context, ctr *testcontainers.DockerContainer) string {
	const keep = 3
	info, err := ctr.Inspect(ctx)
	if err != nil {
		return fmt.Sprintf("\n(its state can't be read: %v)", err)
	}
	if info.State == nil {
		return "\n(Docker reported no state for it)"
	}
	report := fmt.Sprintf("\nIts state is %s", info.State.Status)
	h := info.State.Health
	if h == nil {
		return report + ", with no health check results."
	}
	report += fmt.Sprintf(", and its health %s after %d failed checks in a row.", h.Status, h.FailingStreak)
	for _, r := range h.Log[max(0, len(h.Log)-keep):] {
		out := strings.TrimSpace(r.Output)
		if len(out) > 300 {
			out = strings.ToValidUTF8(out[:300], "") + "…"
		}
		report += fmt.Sprintf("\nHealth check at %s exited %d: %q", r.Start.UTC().Format(time.TimeOnly), r.ExitCode, out)
	}
	return report
}

// logTail returns the end of a container's log.
func logTail(ctx context.Context, ctr *testcontainers.DockerContainer) string {
	const keep = 40
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
	if len(lines) == 0 {
		lines = []string{"(its log is empty)"}
	}
	tail := "\nThe end of its log:\n" + quoteLines(lines)
	if err := sc.Err(); err != nil {
		tail += fmt.Sprintf("\n(reading the rest of its log failed: %v)", err)
	}
	return tail
}
