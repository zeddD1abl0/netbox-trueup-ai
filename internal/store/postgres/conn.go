package postgres

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
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
	cfg, err := pgxpool.ParseConfig(u.Reveal())
	if err != nil {
		return nil, parseError(err)
	}
	conn := cfg.ConnConfig
	if conn.ConnectTimeout == 0 {
		conn.ConnectTimeout = connectTimeout
	}
	if conn.RuntimeParams["application_name"] == "" {
		conn.RuntimeParams["application_name"] = applicationName
	}
	// A search_path in the URL's options, the role's settings or a pooler's
	// would otherwise decide where tables go, so it is set as each
	// connection opens instead of passed along as a startup parameter,
	// which some poolers drop.
	conn.AfterConnect = setSearchPath
	return cfg, nil
}

// setSearchPath makes TrueUp's schema the only one where a name without a
// schema is looked for. That also keeps objects that others can create, as
// in public, from standing in for TrueUp's.
func setSearchPath(ctx context.Context, conn *pgconn.PgConn) error {
	if _, err := conn.Exec(ctx, "SET search_path TO "+Schema).ReadAll(); err != nil {
		return fmt.Errorf("set the search path: %w", err)
	}
	return nil
}

// parseError describes why pgx can't read database.url. pgx's own message
// repeats the URL, with the password hidden but the user name not, and the
// error from url.Parse that it wraps repeats it whole, so only what follows
// is kept, when it is safe.
func parseError(err error) error {
	const msg = "database.url can't be used to connect to PostgreSQL"
	inner := errors.Unwrap(err)
	if _, isURL := errors.AsType[*url.Error](inner); inner == nil || isURL {
		return &ConfigError{msg + ": check its host, port and parameters"}
	}
	return &ConfigError{msg + ": " + inner.Error()}
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
