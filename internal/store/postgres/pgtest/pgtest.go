// Package pgtest runs a disposable PostgreSQL for TrueUp's storage tests, and
// gives each test a database of its own, so that tests run in parallel
// without sharing tables or advisory locks, which belong to a database.
//
// The image is the one compose.yaml pins. Start publishes PostgreSQL's port on
// 127.0.0.1 only, so it needs a Docker daemon on this machine, as
// internal/netboxtest does. Start one server per test binary, in TestMain,
// and share it.
package pgtest

import (
	"context"
	_ "embed" // compose.yaml
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.yaml.in/yaml/v3"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/localdocker"
)

//go:embed compose.yaml
var composeFile []byte

// The disposable server's superuser.
const (
	Superuser         = "postgres"
	SuperuserPassword = "pgtest-only" // a dummy, for a disposable server on 127.0.0.1
)

// Server is a running PostgreSQL.
type Server struct {
	// Host and Port are where it listens, on this machine.
	Host, Port string

	container *testcontainers.DockerContainer
	// admin is connected to the postgres database as the superuser.
	admin     *pgxpool.Pool
	databases atomic.Int64
}

// Image returns the image that compose.yaml pins, by digest.
func Image() (string, error) {
	var f struct {
		Services map[string]struct {
			Image string `yaml:"image"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(composeFile, &f); err != nil {
		return "", fmt.Errorf("compose.yaml: %w", err)
	}
	image := f.Services["postgres"].Image
	if _, digest, ok := strings.Cut(image, "@sha256:"); !ok || len(digest) != 64 {
		return "", fmt.Errorf("compose.yaml: the postgres service's image %q isn't pinned by digest", image)
	}
	return image, nil
}

// Start starts a fresh PostgreSQL and returns once it accepts connections.
// If it fails, it removes what it started.
func Start(ctx context.Context) (_ *Server, err error) {
	image, err := Image()
	if err != nil {
		return nil, err
	}
	if err := localdocker.Check("PostgreSQL"); err != nil {
		return nil, err
	}
	const port = "5432/tcp"
	ctr, err := testcontainers.Run(ctx, image,
		testcontainers.WithEnv(map[string]string{"POSTGRES_PASSWORD": SuperuserPassword}),
		testcontainers.WithExposedPorts(port),
		testcontainers.WithHostConfigModifier(func(hc *container.HostConfig) {
			hc.PortBindings = localdocker.Bindings([]string{port})
		}),
		// Durability doesn't matter for a server that tests throw away.
		testcontainers.WithCmd("postgres", "-c", "fsync=off", "-c", "synchronous_commit=off", "-c", "full_page_writes=off"),
		// The image starts a server once to set itself up, and then again
		// for good.
		testcontainers.WithWaitStrategyAndDeadline(2*time.Minute, wait.ForAll(
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
			wait.ForListeningPort(port),
		)),
	)
	s := &Server{Host: "127.0.0.1", container: ctr}
	defer func() {
		if err != nil {
			stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			defer cancel()
			err = errors.Join(err, s.Stop(stopCtx))
		}
	}()
	if err != nil {
		return nil, fmt.Errorf("start PostgreSQL: %w", err)
	}
	info, err := ctr.Inspect(ctx)
	if err != nil {
		return nil, fmt.Errorf("inspect PostgreSQL's container: %w", err)
	}
	if info.NetworkSettings == nil {
		return nil, errors.New("no network settings reported for PostgreSQL's container")
	}
	if err := localdocker.CheckPorts(info.NetworkSettings.Ports); err != nil {
		return nil, err
	}
	mapped, err := ctr.MappedPort(ctx, port)
	if err != nil {
		return nil, fmt.Errorf("find PostgreSQL's port: %w", err)
	}
	s.Port = mapped.Port()
	if s.admin, err = pgxpool.New(ctx, s.URL(Superuser, SuperuserPassword, "postgres")); err != nil {
		return nil, fmt.Errorf("connect to PostgreSQL: %w", err)
	}
	if err := s.admin.Ping(ctx); err != nil {
		return nil, fmt.Errorf("connect to PostgreSQL: %w", err)
	}
	return s, nil
}

// Stop removes the server. If a test binary dies before calling it,
// testcontainers-go's reaper removes it.
func (s *Server) Stop(ctx context.Context) error {
	if s.admin != nil {
		s.admin.Close()
	}
	if s.container == nil {
		return nil
	}
	if err := s.container.Terminate(ctx); err != nil {
		return fmt.Errorf("remove PostgreSQL: %w", err)
	}
	return nil
}

// Run starts a server, runs the tests with it in *srv, and stops it,
// returning the exit code for os.Exit. Call it from TestMain.
func Run(m *testing.M, srv **Server) int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	s, err := Start(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "start PostgreSQL:", err)
		return 1
	}
	*srv = s
	code := m.Run()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Minute)
	defer stopCancel()
	if err := s.Stop(stopCtx); err != nil {
		fmt.Fprintln(os.Stderr, "stop PostgreSQL:", err)
		code = 1
	}
	return code
}

// URL returns the URL of a database on the server, for a user and password.
// TLS is off, since the server is on this machine and has no certificate.
func (s *Server) URL(user, password, database string) string {
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(user, password),
		Host:     net.JoinHostPort(s.Host, s.Port),
		Path:     "/" + database,
		RawQuery: "sslmode=disable",
	}
	return u.String()
}

// NewDatabase creates an empty database for the test, and returns its name.
// Its text sorts by ICU's English rules, not by bytes, so that the storage
// tests show that TrueUp sorts by bytes whatever the database's locale.
func (s *Server) NewDatabase(t testing.TB) string {
	t.Helper()
	name := fmt.Sprintf("test_%d", s.databases.Add(1))
	s.Exec(t, "postgres", "CREATE DATABASE "+name+" TEMPLATE template0 ENCODING 'UTF8' LOCALE_PROVIDER icu ICU_LOCALE 'en-US' LOCALE 'C'")
	return name
}

// NewURL creates an empty database for the test, as NewDatabase does, and
// returns the URL that opens it as the superuser.
func (s *Server) NewURL(t testing.TB) config.Secret {
	t.Helper()
	return config.NewSecret(s.URL(Superuser, SuperuserPassword, s.NewDatabase(t)))
}

// Exec runs SQL in a database as the superuser, as a database administrator
// would, failing the test if it fails.
func (s *Server) Exec(t testing.TB, database, sql string) {
	t.Helper()
	if database == "postgres" {
		if _, err := s.admin.Exec(t.Context(), sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return
	}
	conn, err := pgx.Connect(t.Context(), s.URL(Superuser, SuperuserPassword, database))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(t.Context())) }()
	if _, err := conn.Exec(t.Context(), sql); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// Pool connects to a database as the superuser, without TrueUp's settings,
// to look at it or change it as no release of TrueUp would. It closes when
// the test ends.
func (s *Server) Pool(t testing.TB, database string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), s.URL(Superuser, SuperuserPassword, database))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}
