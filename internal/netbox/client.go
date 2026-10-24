// Package netbox is TrueUp's client for NetBox's REST API, built on net/http
// alone (docs/adr/0019-own-netbox-rest-client.md). In milestone 1 it only
// reads: it has no method that writes, and every request it sends is a GET.
//
// Connect checks that NetBox runs a release TrueUp supports and that it
// accepts the token, before anything else is read. Listings use NetBox's
// cursor pagination, and children of many parents are listed in one request.
// Each request has a timeout, requests in flight are capped, and failed reads
// are retried with backoff.
//
// Everything NetBox sends is untrusted input (boundary B2 in
// docs/architecture.md). Answers are limited in size, must be JSON, and are
// checked for every property the client relies on, such as IDs in order.
// Errors quote NetBox's own messages with %q, cut short and with the token
// removed. TLS verification can't be turned off, redirects are never
// followed, and only https goes through a proxy, which tunnels it, so the
// token goes only to the configured URL. The token is never
// logged: a request's Authorization header is logged as its scheme and
// "[redacted]".
package netbox

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/version"
)

// SupportedVersion is the NetBox release line TrueUp works with: any 4.7.x
// release, and nothing else (docs/adr/0003-initial-netbox-target.md).
const SupportedVersion = "4.7"

// Defaults for Options left at zero. They match the defaults of the
// netbox.* settings in internal/config, which a test checks. Retries has no
// such default: zero means none.
const (
	defaultTimeout         = 30 * time.Second
	defaultMaxRequests     = 4
	defaultMaxResponseSize = 32 << 20 // 32 MiB
)

// Fixed limits, not settings.
const (
	// pageSize is how many objects a listing asks for per page. It is
	// NetBox's default MAX_PAGE_SIZE. NetBox quietly gives fewer if its
	// MAX_PAGE_SIZE is lower, which List copes with.
	pageSize = 1000
	// maxFilterBytes caps the filter values ListByParent puts in one URL.
	// The whole request line then stays well under 4094 bytes, gunicorn's
	// default limit_request_line, which is among the smallest in common use.
	maxFilterBytes = 3000
	dialTimeout    = 10 * time.Second
)

// ErrTokenRejected is returned, wrapped, by Connect when NetBox refuses the
// token: it is wrong, disabled, expired, restricted to other addresses, or
// its user is inactive, or a proxy in front of NetBox didn't pass it on.
var ErrTokenRejected = errors.New("NetBox refused the token")

// VersionError is returned by Connect when NetBox runs a release TrueUp
// doesn't support, or reports a version that can't be read.
type VersionError struct {
	// Version is what NetBox reported, which may not be a version at all.
	Version string
}

func (e *VersionError) Error() string {
	v := strconv.Quote(e.Version)
	if versionPattern.MatchString(e.Version) {
		v = e.Version
	}
	return fmt.Sprintf("NetBox %s isn't supported: TrueUp works with NetBox %s only", v, SupportedVersion)
}

// Options configure a Client. URL and Token are required.
type Options struct {
	// URL is NetBox's base URL, as netbox.url gives it, such as
	// https://netbox.example.com or https://example.com/netbox.
	URL string
	// Token is a v2 token, nbt_<key>.<token>, or a v1 token, which still
	// works on NetBox 4.7 but is deprecated.
	Token config.Secret
	// CACerts are trusted for NetBox's certificate, as well as the system's.
	CACerts []*x509.Certificate
	// Timeout limits each request, reading the answer included. Zero means
	// 30s.
	Timeout time.Duration
	// MaxRequests caps how many requests are in flight at once. Zero means 4.
	MaxRequests int
	// Retries is how many more times a failed read is tried. Zero means none.
	Retries int
	// MaxResponseSize is the most bytes read of one answer. Zero means 32 MiB.
	MaxResponseSize int64
	// Logger gets a debug record for each request, and a warning for each
	// retry and for a v1 token. Nil means no logging.
	Logger *slog.Logger
}

// Info describes the NetBox a Client is connected to.
type Info struct {
	// URL is NetBox's base URL, as Options gave it but without a trailing
	// slash.
	URL string
	// Version is NetBox's release, such as 4.7.2, and APIVersion its REST
	// API's version, such as 4.7.
	Version, APIVersion string
	// User is the name of the user the token belongs to.
	User string
	// TokenVersion is 2 for a v2 token, or 1 for a deprecated v1 token.
	TokenVersion int
	// Warnings are problems that don't stop the client, such as a v1 token.
	// Connect also logs them.
	Warnings []string
}

// Client reads from one NetBox. It is safe for concurrent use.
type Client struct {
	base         *url.URL // without a trailing slash in its path
	token        config.Secret
	scheme       string // the Authorization scheme: Bearer for v2, Token for v1
	tokenVersion int
	http         *http.Client
	slots        chan struct{} // holds one value per request in flight
	timeout      time.Duration
	retries      int
	maxResponse  int64
	log          *slog.Logger
	userAgent    string
	apiVersion   string // set by Connect, then required of every answer
	info         Info
	warnings     []string // found before connecting, for Info

	// Fixed outside tests.
	pageSize       int
	maxFilterBytes int
	backoff        backoff
	sleep          func(context.Context, time.Duration) error
	now            func() time.Time
}

// Connect returns a client for the NetBox at opts.URL, once it has checked
// that NetBox runs a supported release (SupportedVersion) and accepts the
// token. A release it doesn't support gives a *VersionError, and a refused
// token an error wrapping ErrTokenRejected.
func Connect(ctx context.Context, opts Options) (*Client, error) {
	c, err := newClient(opts)
	if err != nil {
		return nil, err
	}
	if err := c.connect(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

// connect checks NetBox's version and the token, and records what it found.
func (c *Client) connect(ctx context.Context) error {
	version, err := c.checkVersion(ctx)
	if err != nil {
		return err
	}
	user, err := c.checkToken(ctx)
	if err != nil {
		return err
	}
	c.info = Info{
		URL:          c.base.String(),
		Version:      version,
		APIVersion:   c.apiVersion,
		User:         user,
		TokenVersion: c.tokenVersion,
		Warnings:     slices.Clone(c.warnings),
	}
	if c.tokenVersion == 1 {
		msg := "netbox.token is a v1 token, which NetBox deprecated in 4.6 and removes in 5.0: provision a v2 token (nbt_<key>.<token>) instead"
		c.info.Warnings = append(c.info.Warnings, msg)
		c.log.Warn(msg)
	}
	c.log.Debug("connected to NetBox", "url", c.info.URL, "version", version, "user", user, "token_version", c.tokenVersion)
	return nil
}

// newClient checks opts and builds a client without talking to NetBox.
func newClient(opts Options) (*Client, error) {
	base, err := baseURL(opts.URL)
	if err != nil {
		return nil, err
	}
	scheme, tokenVersion, err := authScheme(opts.Token)
	if err != nil {
		return nil, err
	}
	switch {
	case opts.Timeout < 0, opts.MaxRequests < 0, opts.Retries < 0, opts.MaxResponseSize < 0:
		return nil, errors.New("NetBox client options must not be negative")
	case opts.Timeout == 0:
		opts.Timeout = defaultTimeout
	}
	if opts.MaxRequests == 0 {
		opts.MaxRequests = defaultMaxRequests
	}
	if opts.MaxResponseSize == 0 {
		opts.MaxResponseSize = defaultMaxResponseSize
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	roots, warning := rootCAs(opts.CACerts)
	var warnings []string
	if warning != "" {
		warnings = append(warnings, warning)
		logger.Warn(warning)
	}

	transport := &http.Transport{
		Proxy:       httpsProxy(http.ProxyFromEnvironment),
		DialContext: (&net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}).DialContext,
		// security: verification is always on (B2 in docs/architecture.md):
		// there is no setting to turn it off, only one to trust another CA.
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
		TLSHandshakeTimeout: dialTimeout,
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        opts.MaxRequests,
		MaxIdleConnsPerHost: opts.MaxRequests,
		IdleConnTimeout:     90 * time.Second,
	}
	return &Client{
		base:         base,
		token:        opts.Token,
		scheme:       scheme,
		tokenVersion: tokenVersion,
		http: &http.Client{
			Transport: transport,
			// NetBox's API doesn't redirect. Following a redirect could send
			// the token to another host, or over plain http, so the client
			// reports it instead.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		slots:          make(chan struct{}, opts.MaxRequests),
		timeout:        opts.Timeout,
		retries:        opts.Retries,
		maxResponse:    opts.MaxResponseSize,
		log:            logger,
		warnings:       warnings,
		userAgent:      "netbox-trueup-ai/" + version.String(),
		pageSize:       pageSize,
		maxFilterBytes: maxFilterBytes,
		backoff:        defaultBackoff,
		sleep:          sleep,
		now:            time.Now,
	}, nil
}

// Info describes the NetBox the client is connected to.
func (c *Client) Info() Info {
	info := c.info
	info.Warnings = slices.Clone(c.info.Warnings)
	return info
}

// baseURL checks NetBox's URL as the netbox.url setting does, since options
// can come from elsewhere, such as tests, and drops trailing slashes from its
// path.
func baseURL(raw string) (*url.URL, error) {
	if problem := config.NetBoxURLProblem(raw); problem != "" {
		return nil, errors.New("NetBox's URL " + problem)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("NetBox's URL can't be read") // NetBoxURLProblem parsed it, so this can't happen
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u, nil
}

// authScheme returns the Authorization scheme for a token, and its version.
// NetBox reads a token that starts with nbt_ as v2, nbt_<key>.<token>, and
// any other as v1; it accepts either scheme for either version, but these
// are the documented ones (TokenAuthentication in
// netbox/netbox/api/authentication.py). Errors never repeat the token.
func authScheme(token config.Secret) (scheme string, tokenVersion int, err error) {
	t := token.Reveal()
	if t == "" {
		return "", 0, errors.New("no NetBox token: set netbox.token or netbox.token_file")
	}
	if problem := config.NetBoxTokenProblem(t); problem != "" {
		return "", 0, errors.New("the NetBox token " + problem)
	}
	if strings.HasPrefix(t, "nbt_") {
		return "Bearer", 2, nil
	}
	return "Token", 1, nil
}

// httpsProxy returns a Proxy function for http.Transport that takes the proxy
// from fromEnv, normally http.ProxyFromEnvironment, which reads HTTPS_PROXY
// and NO_PROXY, and never uses one for this machine. It uses a proxy only
// for https, which the proxy tunnels without seeing the token. A proxy would
// see a plain http request whole, token included, so those go straight to
// NetBox.
func httpsProxy(fromEnv func(*http.Request) (*url.URL, error)) func(*http.Request) (*url.URL, error) {
	return func(req *http.Request) (*url.URL, error) {
		if req.URL.Scheme != "https" {
			return nil, nil
		}
		return fromEnv(req)
	}
}

// rootCAs returns the certificates to verify NetBox's against. With no extra
// ones it returns nil, which leaves verification to the system, through its
// own verifier on macOS and Windows. If the system's certificates can't be
// read, it trusts only the extra ones, and says so in warning.
func rootCAs(extra []*x509.Certificate) (pool *x509.CertPool, warning string) {
	if len(extra) == 0 {
		return nil, ""
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		// security: trusting only the bundle is stricter, not looser, and a
		// NetBox certificate from another CA then fails verification.
		pool = x509.NewCertPool()
		warning = fmt.Sprintf("the system's trusted certificates can't be read (%v), so only those in netbox.ca_bundle are trusted", err)
	}
	for _, cert := range extra {
		pool.AddCert(cert)
	}
	return pool, warning
}

// versionPattern matches the release numbers NetBox reports, such as 4.7.2.
// RELEASE.version in netbox/netbox/settings.py comes from release.yaml, with
// any designation such as beta1 kept apart, in netbox-full-version.
var versionPattern = regexp.MustCompile(`^([0-9]{1,4})\.([0-9]{1,4})\.([0-9]{1,4})$`)

// checkVersion reads NetBox's status and refuses a release TrueUp doesn't
// support (ADR 0003). A token NetBox refuses fails here already: REST
// framework authenticates every request before it checks permissions, and
// with LOGIN_REQUIRED on, as it is by default in 4.7, /api/status/ needs an
// authenticated user anyway (StatusView and IsAuthenticatedOrLoginNotRequired
// in netbox/netbox/api/views.py and authentication.py).
func (c *Client) checkVersion(ctx context.Context) (string, error) {
	var status struct {
		Version *string `json:"netbox-version"`
	}
	header, err := c.getJSON(ctx, statusPath, nil, &status)
	if err != nil {
		if refusedByNetBox(err) {
			return "", fmt.Errorf("%w: %w", ErrTokenRejected, err)
		}
		return "", fmt.Errorf("read NetBox's status: %w", err)
	}
	if status.Version == nil {
		return "", fmt.Errorf("GET %s: the answer has no netbox-version: is %s NetBox's address?", statusPath, c.base)
	}
	v := c.redact(*status.Version)
	m := versionPattern.FindStringSubmatch(v)
	if m == nil || m[1]+"."+m[2] != SupportedVersion {
		return "", &VersionError{Version: cut(v)}
	}
	// The middleware sets API-Version on every API answer to the release's
	// major and minor version (CoreMiddleware in netbox/netbox/middleware.py,
	// REST_FRAMEWORK_VERSION in settings.py). Every later answer must carry
	// the same, which catches a NetBox upgraded while TrueUp runs.
	if got := header.Get("API-Version"); got != SupportedVersion {
		return "", fmt.Errorf("GET %s: NetBox %s answered with API-Version %q, not %s", statusPath, v, cut(c.redact(got)), SupportedVersion)
	}
	c.apiVersion = SupportedVersion
	return v, nil
}

// checkToken asks NetBox whose token it is. /api/authentication-check/
// always needs an authenticated user, whatever LOGIN_REQUIRED says
// (AuthenticationCheckView in netbox/netbox/api/views.py). A token NetBox
// refuses fails every request, /api/status/ included, since REST framework
// authenticates before it checks permissions. But a proxy that drops the
// Authorization header, as Apache does without WSGIPassAuthorization, leaves
// a request without a token, which /api/status/ answers when LOGIN_REQUIRED
// is off. This check catches that.
func (c *Client) checkToken(ctx context.Context) (user string, err error) {
	var who struct {
		Username *string `json:"username"`
	}
	if _, err := c.getJSON(ctx, authCheckPath, nil, &who); err != nil {
		if refusedByNetBox(err) {
			return "", fmt.Errorf("%w: %w; if a proxy is in front of NetBox, check that it passes the Authorization header on", ErrTokenRejected, err)
		}
		return "", fmt.Errorf("check the token: %w", err)
	}
	if who.Username == nil || *who.Username == "" {
		return "", fmt.Errorf("GET %s: the answer names no user", authCheckPath)
	}
	return cut(c.redact(*who.Username)), nil
}
