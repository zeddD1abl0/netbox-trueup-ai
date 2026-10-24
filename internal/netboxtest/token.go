package netboxtest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
)

// maxResponse caps how much of a NetBox response netboxtest reads. A token
// response is well under a kilobyte.
const maxResponse = 1 << 20

// ProvisionToken creates a v2 API token for the superuser of a NetBox started
// from this package's compose.yaml, which may be the one `make netbox-up`
// started, and returns it in the form nbt_<key>.<token>. description is
// recorded on the token in NetBox.
//
// It refuses a URL that isn't on this machine, since it sends the stack's
// superuser password: netboxtest only ever talks to a disposable local NetBox.
func ProvisionToken(ctx context.Context, baseURL, description string) (config.Secret, error) {
	username, password, err := Superuser()
	if err != nil {
		return config.Secret{}, err
	}
	return provisionToken(ctx, baseURL, username, password, description)
}

// Superuser returns the user name and password of the superuser that
// compose.yaml creates: dummy values, for this disposable stack only.
func Superuser() (username, password string, err error) {
	st, _, err := parseStack(composeFile)
	if err != nil {
		return "", "", fmt.Errorf("compose.yaml: %w", err)
	}
	env := st.Services[netboxService].Environment
	return env["SUPERUSER_NAME"], env["SUPERUSER_PASSWORD"], nil
}

// provisionToken uses NetBox's token provisioning endpoint, which exchanges a
// user name and password for a new token. In NetBox 4.7 a new token is v2
// unless the request asks for v1, and NetBox generates its value
// (TokenProvisionView and TokenProvisionSerializer in
// netbox/users/api/views.py and serializers_/tokens.py). v2 tokens need
// API_TOKEN_PEPPERS, which compose.yaml sets.
func provisionToken(ctx context.Context, baseURL, username, password, description string) (config.Secret, error) {
	endpoint, err := localEndpoint(baseURL, "/api/users/tokens/provision/")
	if err != nil {
		return config.Secret{}, err
	}
	body, err := json.Marshal(map[string]any{
		"username":      username,
		"password":      password,
		"description":   description,
		"write_enabled": true, // tests seed NetBox with it
	})
	if err != nil {
		return config.Secret{}, fmt.Errorf("encode token request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return config.Secret{}, fmt.Errorf("token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	status, respBody, err := do(req)
	if err != nil {
		return config.Secret{}, fmt.Errorf("provision a token: %w", err)
	}
	switch {
	case status >= 400:
		return config.Secret{}, fmt.Errorf("provision a token: NetBox answered %d, not 201: %q", status, snippet(respBody))
	case status != http.StatusCreated:
		// Any other answer might hold a token, so don't quote it.
		return config.Secret{}, fmt.Errorf("provision a token: NetBox answered %d, not 201", status)
	}

	var tok struct {
		Version      int     `json:"version"`
		Key          string  `json:"key"`
		Token        string  `json:"token"`
		Enabled      *bool   `json:"enabled"`
		WriteEnabled *bool   `json:"write_enabled"`
		Expires      *string `json:"expires"`
	}
	if err := json.Unmarshal(respBody, &tok); err != nil {
		return config.Secret{}, fmt.Errorf("provision a token: decode NetBox's answer: %w", err)
	}
	switch {
	case tok.Version != 2:
		return config.Secret{}, fmt.Errorf("provision a token: NetBox made a v%d token, not v2; check API_TOKEN_PEPPERS", tok.Version)
	case !isTokenPart(tok.Key) || !isTokenPart(tok.Token):
		// Don't quote them: one of them is the secret.
		return config.Secret{}, errors.New("provision a token: NetBox's answer has no usable key and token")
	// A token that is disabled, read-only or expiring would fail tests later
	// with a 403 that doesn't say why.
	case tok.Enabled == nil || !*tok.Enabled:
		return config.Secret{}, errors.New("provision a token: NetBox made a disabled token")
	case tok.WriteEnabled == nil || !*tok.WriteEnabled:
		return config.Secret{}, errors.New("provision a token: NetBox made a read-only token")
	case tok.Expires != nil:
		return config.Secret{}, fmt.Errorf("provision a token: NetBox made a token that expires at %q", *tok.Expires)
	}
	return config.NewSecret("nbt_" + tok.Key + "." + tok.Token), nil
}

// localEndpoint joins baseURL and path, and checks that baseURL is an http URL
// on this machine.
func localEndpoint(baseURL, path string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("NetBox URL: %w", err)
	}
	if u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", fmt.Errorf("NetBox URL %q must be http://HOST:PORT, with nothing else", baseURL)
	}
	if !isLoopbackHost(u.Hostname()) {
		return "", fmt.Errorf("NetBox URL %q isn't on this machine; netboxtest only talks to a disposable local NetBox", baseURL)
	}
	return u.Scheme + "://" + u.Host + path, nil
}

// client only ever connects to this machine, whatever a name resolves to, and
// never through a proxy. It never follows redirects either, so a request with
// credentials can't be sent on somewhere else.
var client = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		Proxy:       nil,
		DialContext: (&net.Dialer{Timeout: 10 * time.Second, Control: dialLoopbackOnly}).DialContext,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// dialLoopbackOnly refuses to connect to an address that isn't loopback. It
// sees the resolved address, so it also covers a name such as localhost.
func dialLoopbackOnly(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("refuse to connect to %s: %w", address, err)
	}
	if ip, err := netip.ParseAddr(host); err != nil || !ip.Unmap().IsLoopback() {
		return fmt.Errorf("refuse to connect to %s: netboxtest only talks to this machine", address)
	}
	return nil
}

// do sends req and returns the status and up to maxResponse bytes of body.
func do(req *http.Request) (int, []byte, error) {
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return 0, nil, fmt.Errorf("read NetBox's answer: %w", err)
	}
	if len(body) > maxResponse {
		return 0, nil, fmt.Errorf("NetBox's answer is over %d bytes", maxResponse)
	}
	return resp.StatusCode, body, nil
}

// snippet returns the start of a response body for an error message, to be
// quoted with %q since it comes from NetBox. NetBox's errors don't repeat the
// password.
func snippet(body []byte) string {
	const limit = 300
	s := strings.TrimSpace(string(body))
	if len(s) > limit {
		s = strings.ToValidUTF8(s[:limit], "") + "…"
	}
	return s
}

// isTokenPart reports whether s could be a token key or value. NetBox makes
// both from ASCII letters and digits (TOKEN_CHARSET in netbox/users/constants.py).
func isTokenPart(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}
