package netbox

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
)

func TestConnect(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	c := connected(t, f, Options{URL: f.srv.URL + "//"}, nil)

	want := Info{URL: f.srv.URL, Version: "4.7.2", APIVersion: "4.7", User: "trueup", TokenVersion: 2}
	if diff := cmp.Diff(want, c.Info()); diff != "" {
		t.Errorf("Info (-want +got):\n%s", diff)
	}
	reqs := f.recorded()
	if diff := cmp.Diff([]string{statusPath, authCheckPath}, f.paths()); diff != "" {
		t.Errorf("requests (-want +got):\n%s", diff)
	}
	for _, r := range reqs {
		if r.authorization != "Bearer "+testToken || r.accept != "application/json" || !strings.HasPrefix(r.userAgent, "netbox-trueup-ai/") {
			t.Errorf("%s sent Authorization of %d bytes, Accept %q, User-Agent %q", r.path, len(r.authorization), r.accept, r.userAgent)
		}
	}
}

// NetBox can live under a path, with BASE_PATH.
func TestConnectUnderAPath(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.handle = func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasPrefix(r.URL.Path, "/netbox/api/") {
			t.Errorf("request for %s, not under /netbox/", r.URL.Path)
		}
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/netbox")
		return false
	}
	connected(t, f, Options{URL: f.srv.URL + "/netbox/"}, nil)
}

func TestConnectChecksTheVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		version, apiVersion string
		want                string // the error, or "" for none
	}{
		{version: "4.7.0", apiVersion: "4.7"},
		{version: "4.7.13", apiVersion: "4.7"},
		{version: "4.6.5", apiVersion: "4.6", want: "NetBox 4.6.5 isn't supported: TrueUp works with NetBox 4.7 only"},
		{version: "4.8.0", apiVersion: "4.8", want: "NetBox 4.8.0 isn't supported: TrueUp works with NetBox 4.7 only"},
		{version: "5.0.0", apiVersion: "5.0", want: "NetBox 5.0.0 isn't supported: TrueUp works with NetBox 4.7 only"},
		{version: "4.70.1", apiVersion: "4.70", want: "NetBox 4.70.1 isn't supported: TrueUp works with NetBox 4.7 only"},
		{version: "4.7", apiVersion: "4.7", want: `NetBox "4.7" isn't supported: TrueUp works with NetBox 4.7 only`},
		{version: "v4.7.2", apiVersion: "4.7", want: `NetBox "v4.7.2" isn't supported: TrueUp works with NetBox 4.7 only`},
		{version: "4.7.2\x1b[2J", apiVersion: "4.7", want: `NetBox "4.7.2\x1b[2J" isn't supported: TrueUp works with NetBox 4.7 only`},
		{version: "4.7.2", apiVersion: "4.6", want: `GET /api/status/: NetBox 4.7.2 answered with API-Version "4.6", not 4.7`},
		{version: "4.7.2", apiVersion: "", want: `GET /api/status/: NetBox 4.7.2 answered with API-Version "", not 4.7`},
	}
	for _, tt := range tests {
		t.Run(tt.version+" "+tt.apiVersion, func(t *testing.T) {
			t.Parallel()
			f := newFake(t)
			f.version, f.apiVersion = tt.version, tt.apiVersion
			err := newTestClient(t, f, Options{}, nil).connect(t.Context())
			if tt.want == "" {
				if err != nil {
					t.Fatalf("connect: %v", err)
				}
				return
			}
			if err == nil || err.Error() != tt.want {
				t.Fatalf("connect error = %v, want %q", err, tt.want)
			}
			if _, isVersion := errors.AsType[*VersionError](err); isVersion != strings.Contains(tt.want, "isn't supported") {
				t.Errorf("connect error is a *VersionError: %v", isVersion)
			}
			if got := f.paths(); len(got) != 1 {
				t.Errorf("requests = %v, want only the status", got)
			}
		})
	}
}

func TestConnectNeedsAVersion(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.handle = func(w http.ResponseWriter, r *http.Request) bool {
		f.writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
		return true
	}
	err := newTestClient(t, f, Options{}, nil).connect(t.Context())
	if err == nil || !strings.Contains(err.Error(), "the answer has no netbox-version") {
		t.Errorf("connect error = %v, want one about the missing version", err)
	}
}

func TestConnectRejectsTheToken(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.authorization = "Bearer nbt_other.token"
	err := newTestClient(t, f, Options{}, nil).connect(t.Context())
	want := `NetBox refused the token: GET /api/status/: NetBox answered 403 Forbidden: "Invalid v2 token"`
	if !errors.Is(err, ErrTokenRejected) || err.Error() != want {
		t.Errorf("connect error = %v, want %q", err, want)
	}
}

// A proxy that drops the Authorization header leaves NetBox a request
// without a token, which /api/status/ answers when LOGIN_REQUIRED is off.
// The authentication check catches it.
func TestConnectBehindAProxyThatDropsTheToken(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.statusOpen = true
	f.handle = func(w http.ResponseWriter, r *http.Request) bool {
		r.Header.Del("Authorization")
		return false
	}
	err := newTestClient(t, f, Options{}, nil).connect(t.Context())
	want := `NetBox refused the token: GET /api/authentication-check/: NetBox answered 403 Forbidden: ` +
		`"Authentication credentials were not provided."; if a proxy is in front of NetBox, check that it passes the Authorization header on`
	if !errors.Is(err, ErrTokenRejected) || err.Error() != want {
		t.Errorf("connect error = %v, want %q", err, want)
	}
}

// A 403 that a proxy or firewall gives, without NetBox's API-Version
// header, is no judgement on the token.
func TestConnectRefusedByAProxy(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.handle = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("<html>Access denied</html>"))
		return true
	}
	err := newTestClient(t, f, Options{}, nil).connect(t.Context())
	if errors.Is(err, ErrTokenRejected) {
		t.Errorf("connect error = %v, which blames the token", err)
	}
	want := "read NetBox's status: GET /api/status/: the server answered 403 Forbidden; the answer has no API-Version header, so a proxy or firewall in front of NetBox may have given it"
	if err == nil || err.Error() != want {
		t.Errorf("connect error = %v, want %q", err, want)
	}
}

func TestConnectWarnsAboutV1Tokens(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.authorization = "Token " + testV1Token
	c := connected(t, f, Options{Token: config.NewSecret(testV1Token)}, nil)

	info := c.Info()
	if info.TokenVersion != 1 || len(info.Warnings) != 1 || !strings.Contains(info.Warnings[0], "is a v1 token, which NetBox deprecated in 4.6") {
		t.Errorf("Info = %+v, want token version 1 with a warning", info)
	}
	if !strings.Contains(c.logs.String(), `"level":"WARN","msg":"netbox.token is a v1 token`) {
		t.Errorf("the warning wasn't logged:\n%s", c.logs)
	}
	if strings.Contains(c.logs.String(), testV1Token) {
		t.Errorf("the log holds the token:\n%s", c.logs)
	}
}

func TestNewClientRejectsOptions(t *testing.T) {
	t.Parallel()

	good := Options{URL: "https://netbox.example.com", Token: config.NewSecret(testToken)}
	tests := []struct {
		name   string
		change func(*Options)
		want   string
	}{
		{name: "no URL", change: func(o *Options) { o.URL = "" }, want: "NetBox's URL must be an https or http URL"},
		{name: "a URL without a scheme", change: func(o *Options) { o.URL = "netbox.example.com" }, want: "NetBox's URL must be an https or http URL"},
		{name: "a URL with a password", change: func(o *Options) { o.URL = "https://admin:" + testSecret + "@netbox.example.com" }, want: "must not contain a user name or password"},
		{name: "a URL with a query", change: func(o *Options) { o.URL = "https://netbox.example.com/?x=1" }, want: "must not have a query or fragment"},
		{name: "a URL with an empty query", change: func(o *Options) { o.URL = "https://netbox.example.com/?" }, want: "must not have a query or fragment"},
		{name: "no token", change: func(o *Options) { o.Token = config.Secret{} }, want: "no NetBox token"},
		{name: "a v2 token without its secret", change: func(o *Options) { o.Token = config.NewSecret("nbt_" + testKey) }, want: "isn't in the form nbt_<key>.<token>"},
		{name: "a token with a space", change: func(o *Options) { o.Token = config.NewSecret("nbt_" + testKey + ". " + testSecret) }, want: "contains characters a token can't"},
		{name: "a negative timeout", change: func(o *Options) { o.Timeout = -time.Second }, want: "must not be negative"},
		{name: "negative retries", change: func(o *Options) { o.Retries = -1 }, want: "must not be negative"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			opts := good
			tt.change(&opts)
			_, err := newClient(opts)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("newClient error = %v, want one containing %q", err, tt.want)
			}
			if strings.Contains(err.Error(), testSecret) || strings.Contains(err.Error(), testKey) {
				t.Errorf("error repeats the token: %v", err)
			}
		})
	}
}

// The token is never logged, even at debug level, nor repeated in an error
// when NetBox sends it back.
func TestTokenStaysSecret(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.denied["/api/dcim/manufacturers/"] = true
	f.handle = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/api/dcim/device-types/" {
			return false
		}
		f.writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "you sent " + r.Header.Get("Authorization") + " and " + testSecret})
		return true
	}
	c := connected(t, f, Options{}, nil)
	if _, err := c.CanRead(t.Context(), "/api/dcim/manufacturers/"); err != nil {
		t.Fatalf("CanRead: %v", err)
	}
	err := c.List(t.Context(), "/api/dcim/device-types/", nil, func(json.RawMessage) error { return nil })
	if err == nil {
		t.Fatal("List succeeded, though NetBox answered 400")
	}
	want := `GET /api/dcim/device-types/: NetBox answered 400 Bad Request: "you sent Bearer [redacted] and [redacted]"`
	if err.Error() != want {
		t.Errorf("List error = %q, want %q", err, want)
	}
	logs := c.logs.String()
	if strings.Contains(logs, testSecret) || strings.Contains(logs, testKey) {
		t.Errorf("the log holds the token:\n%s", logs)
	}
	if !strings.Contains(logs, `"authorization":"Bearer [redacted]"`) {
		t.Errorf("requests weren't logged with the Authorization header redacted:\n%s", logs)
	}
	if got := fmt.Sprintf("%v %+v %#v", c.Client, *c.Client, c.Info()); strings.Contains(got, testSecret) {
		t.Errorf("printing the client shows the token: %s", got)
	}
}

func TestTLS(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(f.serve))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0) // the handshakes that fail on purpose
	srv.StartTLS()
	t.Cleanup(srv.Close)

	// Without its CA, the certificate doesn't verify, and that isn't retried.
	c := newTestClient(t, f, Options{URL: srv.URL, Retries: 3}, nil)
	err := c.connect(t.Context())
	if _, ok := errors.AsType[*tls.CertificateVerificationError](err); !ok {
		t.Errorf("connect error = %v, want a certificate verification error", err)
	}
	if len(*c.sleeps) != 0 {
		t.Errorf("a certificate that doesn't verify was retried after %v", *c.sleeps)
	}

	// With it as a CA certificate, it does.
	connected(t, f, Options{URL: srv.URL, CACerts: []*x509.Certificate{srv.Certificate()}}, nil)

	tr, ok := c.http.Transport.(*http.Transport)
	if !ok || tr.TLSClientConfig == nil || tr.TLSClientConfig.InsecureSkipVerify || tr.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		t.Errorf("TLS config = %+v, want verification on and TLS 1.2 or later", tr.TLSClientConfig)
	}
}

func TestPlainHTTPToTLSIsNotRetried(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	c := newTestClient(t, f, Options{URL: strings.Replace(f.srv.URL, "http://", "https://", 1), Retries: 3}, nil)
	err := c.connect(t.Context())
	if err == nil || len(*c.sleeps) != 0 {
		t.Errorf("connect error = %v after waits %v, want an error without retries", err, *c.sleeps)
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.handle = func(w http.ResponseWriter, r *http.Request) bool {
		http.Redirect(w, r, "https://elsewhere.example.com/api/status/", http.StatusFound)
		return true
	}
	err := newTestClient(t, f, Options{}, nil).connect(t.Context())
	want := `read NetBox's status: GET /api/status/: the server answered 302 Found, a redirect to "https://elsewhere.example.com/api/status/", which TrueUp doesn't follow: set netbox.url to the address NetBox's API answers on`
	if err == nil || err.Error() != want {
		t.Errorf("connect error = %v, want %q", err, want)
	}
	if got := f.paths(); len(got) != 1 {
		t.Errorf("requests = %v, want one", got)
	}
}

func TestAnswersMustBeJSON(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.handle = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html>Sign in</html>"))
		return true
	}
	err := newTestClient(t, f, Options{}, nil).connect(t.Context())
	if err == nil || !strings.Contains(err.Error(), `NetBox answered with "text/html; charset=utf-8", not JSON`) {
		t.Errorf("connect error = %v, want one about HTML", err)
	}
}

func TestAnswersAreLimitedInSize(t *testing.T) {
	t.Parallel()

	for _, chunked := range []bool{false, true} {
		t.Run(fmt.Sprintf("chunked: %v", chunked), func(t *testing.T) {
			t.Parallel()
			f := newFake(t)
			f.handle = func(w http.ResponseWriter, r *http.Request) bool {
				body := `{"netbox-version": "4.7.2", "padding": "` + strings.Repeat("x", 2048) + `"}`
				w.Header().Set("Content-Type", "application/json")
				if !chunked {
					w.Header().Set("Content-Length", fmt.Sprint(len(body)))
				}
				_, _ = w.Write([]byte(body))
				return true
			}
			c := newTestClient(t, f, Options{MaxResponseSize: 1024, Retries: 3}, nil)
			err := c.connect(t.Context())
			if !errors.Is(err, errTooLarge) {
				t.Errorf("connect error = %v, want errTooLarge", err)
			}
			if len(*c.sleeps) != 0 {
				t.Errorf("an answer over the limit was retried after %v", *c.sleeps)
			}
		})
	}
}

// Once connected, every answer must carry the API version Connect saw.
func TestAPIVersionMustNotChange(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.add("/api/dcim/manufacturers/", ids(1), nil)
	c := connected(t, f, Options{}, nil)
	f.mu.Lock()
	f.apiVersion = "4.8"
	f.mu.Unlock()
	_, err := c.CanRead(t.Context(), "/api/dcim/manufacturers/")
	if err == nil || !strings.Contains(err.Error(), `NetBox answered with API-Version "4.8", not 4.7: was it upgraded`) {
		t.Errorf("CanRead error = %v, want one about the API version", err)
	}
}

func TestStatusErrorMessages(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("é", 400)
	tests := []struct {
		name        string
		contentType string
		body        string
		want        string
	}{
		{name: "detail", contentType: "application/json", body: `{"detail": "Not found."}`, want: "Not found."},
		{name: "NetBox's server error", contentType: "application/json", body: `{"error": "relation does not exist", "exception": "ProgrammingError"}`, want: "relation does not exist"},
		{name: "a filter error", contentType: "application/json", body: `{"device_type_id": ["Select a valid choice."]}`, want: `{"device_type_id": ["Select a valid choice."]}`},
		{name: "text", contentType: "text/plain; charset=utf-8", body: "Bad gateway\n", want: "Bad gateway"},
		{name: "an HTML page", contentType: "text/html", body: "<html><body>Bad gateway</body></html>"},
		{name: "a long message", contentType: "text/plain", body: long, want: strings.ToValidUTF8(long[:300], "") + "…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := testClient{Client: &Client{token: config.NewSecret(testToken), tokenVersion: 2}}
			header := http.Header{"Content-Type": {tt.contentType}}
			got := c.statusError("/api/x/", answer{status: http.StatusBadGateway, header: header, body: []byte(tt.body)})
			if got.Message != tt.want {
				t.Errorf("Message = %q, want %q", got.Message, tt.want)
			}
		})
	}
}

// Only https goes through a proxy, which tunnels it without seeing the
// token. A proxy would see a plain http request whole.
func TestHTTPSProxy(t *testing.T) {
	t.Parallel()
	proxyURL, _ := url.Parse("http://proxy.example.com:3128")
	proxy := httpsProxy(func(*http.Request) (*url.URL, error) { return proxyURL, nil })
	for target, want := range map[string]*url.URL{
		"https://netbox.example.com/api/status/": proxyURL,
		"http://netbox.example.com/api/status/":  nil,
	} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := proxy(req); got != want || err != nil {
			t.Errorf("proxy for %s = %v, %v; want %v", target, got, err, want)
		}
	}
}

func TestRedact(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, token, text, want string
	}{
		{name: "the token", token: testToken, text: "x " + testToken + " y", want: "x [redacted] y"},
		{name: "the secret part", token: testToken, text: "x " + testSecret, want: "x [redacted]"},
		{name: "a v1 token as JSON escapes it", token: `v1"tokenv1tokenv1token`, text: `{"x": "v1\"tokenv1tokenv1token"}`, want: `{"x": "[redacted]"}`},
		{name: "a secret part too short to be NetBox's", token: "nbt_key.e", text: "the version is 4.7.2", want: "the version is 4.7.2"}, //nolint:gosec // G101: a fake token in a test case
	}
	for _, tt := range tests {
		version := 1
		if strings.HasPrefix(tt.token, "nbt_") {
			version = 2
		}
		c := &Client{token: config.NewSecret(tt.token), tokenVersion: version}
		if got := c.redact(tt.text); got != tt.want {
			t.Errorf("%s: redact(%q) = %q, want %q", tt.name, tt.text, got, tt.want)
		}
	}
}

// The client's defaults for options left at zero match the netbox.*
// settings' defaults, apart from retries.
func TestDefaultsMatchTheSettings(t *testing.T) {
	t.Parallel()
	cfg, err := config.Load(t.Context(), config.Input{})
	if err != nil {
		t.Fatal(err)
	}
	nb := cfg.Settings.NetBox
	if nb.Timeout != defaultTimeout || nb.MaxRequests != defaultMaxRequests || nb.MaxResponseSize != defaultMaxResponseSize {
		t.Errorf("settings default to timeout %v, %d requests and %d bytes; the client to %v, %d and %d",
			nb.Timeout, nb.MaxRequests, nb.MaxResponseSize, defaultTimeout, defaultMaxRequests, defaultMaxResponseSize)
	}
}
