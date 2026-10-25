package git

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v6/plumbing/client"
	"github.com/go-git/go-git/v6/plumbing/transport"
)

const (
	dialTimeout = 10 * time.Second
	// maxRefsSize bounds a repository's list of branches and tags, which
	// go-git holds in memory whole. devicetype-library's 4,500 refs take
	// about 400 KB.
	maxRefsSize = 32 << 20
	// maxRequestPeek is the most of a request's body read to tell whether
	// it asks for refs, which such requests stay well under.
	maxRequestPeek = 64 << 10
)

// newTransport returns the HTTP transport for fetching, which verifies TLS
// against roots, or the system's trusted certificates if roots is nil, and
// takes its proxy from fromEnv, for https only.
func newTransport(roots *x509.CertPool, fromEnv func(*http.Request) (*url.URL, error)) *http.Transport {
	return &http.Transport{
		Proxy:       httpsProxy(fromEnv),
		DialContext: (&net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}).DialContext,
		// security: verification is always on (B1 in
		// docs/architecture.md): there is no setting to turn it off.
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
		TLSHandshakeTimeout: dialTimeout,
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        2,
		IdleConnTimeout:     90 * time.Second,
	}
}

// newHTTPClient returns the HTTP client for one fetch, whose answers count
// against b.
func newHTTPClient(tr http.RoundTripper, b *budget) *http.Client {
	return &http.Client{
		Transport: budgetTransport{base: tr, b: b},
		// go-git refuses redirects itself, given NoFollowRedirects. This
		// refuses them too, should that ever change: a redirect could lead
		// a fetch, and later its credentials, to another host.
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			return fmt.Errorf("the server redirected to %s, which TrueUp doesn't follow: use the repository's own URL", redactURL(req.URL.String()))
		},
	}
}

// httpsProxy returns a Proxy function for http.Transport that takes the proxy
// from fromEnv, normally http.ProxyFromEnvironment, which reads HTTPS_PROXY
// and NO_PROXY and never uses a proxy for this machine. It uses a proxy only
// for https, which the proxy tunnels without seeing the request. Git sources
// are fetched over https only, so nothing else should reach it.
func httpsProxy(fromEnv func(*http.Request) (*url.URL, error)) func(*http.Request) (*url.URL, error) {
	return func(req *http.Request) (*url.URL, error) {
		if req.URL.Scheme != "https" {
			return nil, nil
		}
		return fromEnv(req)
	}
}

// clientOptions configure go-git's transport client for a fetch: our HTTP
// client, no redirects, and every scheme but https refused, so that nothing
// reaches go-git's ssh, git, file or plain http transports, with their own
// defaults, even if a URL slipped past the checks before.
func clientOptions(httpClient *http.Client) []client.Option {
	opts := []client.Option{
		client.WithHTTPClient(httpClient),
		client.WithRedirectPolicy(client.NoFollowRedirects),
	}
	for _, scheme := range []string{"http", "ssh", "git", "file"} {
		opts = append(opts, client.WithTransport(scheme, refused(scheme)))
	}
	return opts
}

// refused is a go-git transport that refuses to connect.
type refused string

func (r refused) Handshake(context.Context, *transport.Request) (transport.Session, error) {
	return nil, fmt.Errorf("%w: TrueUp fetches Git sources over https only, not %s", errScheme, string(r))
}

var errScheme = errors.New("unsupported URL scheme")

// redactURL returns raw without any user name or password, for logs and
// errors. A URL that can't be parsed is replaced entirely, since it can't be
// shown to hold no secret.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(a URL that can't be read)"
	}
	u.User = nil
	return u.String()
}

// budget bounds what one fetch reads from the network: every answer
// together, and each list of refs. go-git holds a list of refs in memory, so
// without a bound a server could send refs until memory ran out.
type budget struct {
	limit   int64
	maxRefs int64

	mu   sync.Mutex
	read int64
	err  error // why the budget stopped a read, or nil
}

// take counts n bytes read.
func (b *budget) take(n int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err == nil && b.read+int64(n) > b.limit {
		b.err = fmt.Errorf("the server sent more than max_fetch_size, %d bytes: raise it, or narrow max_file_size", b.limit)
	}
	b.read += int64(n)
	return b.err
}

// refsTooLarge records that a list of refs went past its limit.
func (b *budget) refsTooLarge() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err == nil {
		b.err = fmt.Errorf("the repository's list of branches and tags is larger than %d bytes", b.maxRefs)
	}
	return b.err
}

// stopped returns why the budget stopped a read, or nil.
func (b *budget) stopped() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.err
}

// budgetTransport counts every answer's body against a budget.
type budgetTransport struct {
	base http.RoundTripper
	b    *budget
}

func (t budgetTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	refs := listsRefs(req)
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	resp.Body = &budgetBody{ReadCloser: resp.Body, b: t.b, refs: refs}
	return resp, nil
}

// listsRefs reports whether a request asks for a repository's refs: the GET
// of info/refs, or a POST of protocol v2's ls-refs command. A request whose
// body can't be read again counts as not asking, and so is bounded only by
// the whole fetch's budget.
func listsRefs(req *http.Request) bool {
	if req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/info/refs") {
		return true
	}
	if req.Method != http.MethodPost || req.GetBody == nil || req.ContentLength > maxRequestPeek {
		return false
	}
	body, err := req.GetBody()
	if err != nil {
		return false
	}
	defer func() { _ = body.Close() }()
	data, err := io.ReadAll(io.LimitReader(body, maxRequestPeek))
	return err == nil && bytes.Contains(data, []byte("command=ls-refs"))
}

// budgetBody is an answer's body that counts against a budget.
type budgetBody struct {
	io.ReadCloser
	b    *budget
	refs bool  // the answer lists refs
	read int64 // bytes read of this answer
}

func (r *budgetBody) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.read += int64(n)
	if r.refs && r.read > r.b.maxRefs {
		return 0, r.b.refsTooLarge()
	}
	if budgetErr := r.b.take(n); budgetErr != nil {
		return 0, budgetErr
	}
	return n, err
}
