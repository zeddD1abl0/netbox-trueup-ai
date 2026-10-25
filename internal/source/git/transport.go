package git

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/go-git/go-git/v6/plumbing/client"
	"github.com/go-git/go-git/v6/plumbing/transport"
)

const dialTimeout = 10 * time.Second

// newHTTPClient returns the HTTP client for fetching, which verifies TLS
// against roots, or the system's trusted certificates if roots is nil.
func newHTTPClient(roots *x509.CertPool, fromEnv func(*http.Request) (*url.URL, error)) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:       httpsProxy(fromEnv),
			DialContext: (&net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}).DialContext,
			// security: verification is always on (B1 in
			// docs/architecture.md): there is no setting to turn it off.
			TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
			TLSHandshakeTimeout: dialTimeout,
			ForceAttemptHTTP2:   true,
			MaxIdleConns:        2,
			IdleConnTimeout:     90 * time.Second,
		},
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

// clientOptions configure go-git's transport client for every fetch: our
// HTTP client, no redirects, and every scheme but https refused, so that
// nothing reaches go-git's ssh, git, file or plain http transports, with
// their own defaults, even if a URL slipped past the checks before.
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
