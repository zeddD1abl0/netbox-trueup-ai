package postgres

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
)

// Schema is the schema that holds TrueUp's tables. Every connection's
// search_path names it alone.
const Schema = "netbox_trueup_ai"

// applicationName names TrueUp's sessions in pg_stat_activity, unless
// database.url, or libpq's PGAPPNAME, names them otherwise.
const applicationName = "netbox-trueup-ai"

// connectTimeout is how long a connection waits for the server, unless
// database.url's connect_timeout, or libpq's PGCONNECT_TIMEOUT, says
// otherwise. pgx alone waits until the operating system gives up, which
// takes minutes.
const connectTimeout = 10 * time.Second

// ConfigError means database.url can't be used to connect: a problem with
// the configuration rather than with the database. Its message never repeats
// the URL, which can hold a password.
type ConfigError struct{ msg string }

func (e *ConfigError) Error() string { return e.msg }

// parseURL reads database.url as pgx does, with libpq's parameters and
// environment variables, TLS among them, and sets what TrueUp needs on every
// connection: its schema as the search_path, a connect timeout and its
// application name.
func parseURL(u config.Secret) (*pgxpool.Config, error) {
	if problem := config.PostgresURLProblem(u.Reveal()); problem != "" {
		return nil, &ConfigError{"database.url " + problem}
	}
	cfg, err := pgxpool.ParseConfig(u.Reveal())
	if err != nil {
		return nil, parseError(err)
	}
	conn := cfg.ConnConfig
	if !connectTimeoutSet(u.Reveal()) {
		conn.ConnectTimeout = connectTimeout
	}
	if conn.RuntimeParams["application_name"] == "" {
		conn.RuntimeParams["application_name"] = applicationName
	}
	conn.AfterConnect = setSession
	return cfg, nil
}

// connectTimeoutSet reports whether the URL or libpq's environment sets
// connect_timeout. 0 is a setting too: it means no limit.
func connectTimeoutSet(raw string) bool {
	if os.Getenv("PGCONNECT_TIMEOUT") != "" {
		return true
	}
	_, query, _ := strings.Cut(raw, "?")
	q, err := url.ParseQuery(query)
	return err == nil && q.Has("connect_timeout")
}

// setSession sets up each of a store's connections as it opens:
//
//   - TrueUp's schema is the only one where a name without a schema is looked
//     for, which also keeps objects that others can create, as in public,
//     from standing in for TrueUp's. A search_path in the URL's options, the
//     role's settings or a pooler's would otherwise decide where tables go,
//     so it is set here rather than passed along as a startup parameter,
//     which some poolers drop.
//   - idle_session_timeout is off, from PostgreSQL 14, which has it: the
//     connection that holds a store's lock is idle for as long as the store
//     is open, and the server would end it, and the lock with it.
func setSession(ctx context.Context, conn *pgconn.PgConn) error {
	return startSession(ctx, conn, Schema)
}

// setLockSession sets up the connection that holds a store's locks, which
// checks TrueUp's schema before anything trusts it: its search_path is
// pg_catalog alone, so that nothing in the schema can stand in for the
// catalog's functions.
func setLockSession(ctx context.Context, conn *pgconn.PgConn) error {
	return startSession(ctx, conn, "pg_catalog")
}

func startSession(ctx context.Context, conn *pgconn.PgConn, searchPath string) error {
	sql := "SELECT pg_catalog.set_config('idle_session_timeout', '0', false) " +
		"WHERE pg_catalog.current_setting('server_version_num')::integer >= 140000; " +
		"SET search_path TO " + searchPath
	if _, err := conn.Exec(ctx, sql).ReadAll(); err != nil {
		return fmt.Errorf("set up the session: %w", err)
	}
	return nil
}

// parseError describes why pgx can't read database.url. pgx's message quotes
// the URL, with the password hidden but the user name not, so it is made
// again without the URL. When the URL itself can't be read, pgx's reason can
// quote a part of it, such as the user name, and url.Parse's, should pgx
// wrap one, quotes it whole, so then only a hint is given.
func parseError(err error) error {
	const msg = "database.url can't be used to connect to PostgreSQL"
	const hint = msg + ": it isn't a URL that libpq would read: check its percent-encoding, host and port"
	pce, ok := errors.AsType[*pgconn.ParseConfigError](err)
	if _, isURL := errors.AsType[*url.Error](err); !ok || isURL {
		return &ConfigError{hint}
	}
	without := *pce
	without.ConnString = ""
	reason := strings.TrimPrefix(without.Error(), "cannot parse ``: ")
	if strings.HasPrefix(reason, "failed to parse as URL") {
		return &ConfigError{hint}
	}
	return &ConfigError{msg + ": " + reason}
}

// location says where the database is, for people and scripts: a postgres
// URL with the hosts, ports and database name alone, never the user name,
// the password or other parameters.
func location(c *pgconn.Config) string {
	type addr struct {
		host string
		port uint16
	}
	// Fallbacks hold the other hosts to try, and with sslmode prefer or
	// allow the same host again, with TLS or without.
	addrs := []addr{{c.Host, c.Port}}
	for _, fb := range c.Fallbacks {
		if a := (addr{fb.Host, fb.Port}); !slices.Contains(addrs, a) {
			addrs = append(addrs, a)
		}
	}
	var hostPorts, hosts, ports []string
	socket := false
	for _, a := range addrs {
		port := strconv.Itoa(int(a.port))
		hostPorts = append(hostPorts, net.JoinHostPort(a.host, port))
		hosts, ports = append(hosts, a.host), append(ports, port)
		socket = socket || strings.HasPrefix(a.host, "/")
	}
	u := url.URL{Scheme: "postgres", Path: "/" + c.Database}
	if socket {
		// A Unix socket's directory goes in the query, as libpq writes it.
		u.RawQuery = url.Values{"host": {strings.Join(hosts, ",")}, "port": {strings.Join(ports, ",")}}.Encode()
	} else {
		u.Host = strings.Join(hostPorts, ",")
	}
	return u.String()
}

// withoutUser returns err without pgx's prefix to a failed connection, which
// names the user, so that messages say where TrueUp connects, not as whom.
func withoutUser(err error) error {
	if ce, ok := errors.AsType[*pgconn.ConnectError](err); ok {
		if inner := ce.Unwrap(); inner != nil {
			return inner
		}
	}
	return err
}
