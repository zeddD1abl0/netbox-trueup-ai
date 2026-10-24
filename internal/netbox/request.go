package netbox

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// API paths the client reads, apart from listings.
const (
	statusPath    = "/api/status/"
	authCheckPath = "/api/authentication-check/"
)

// maxRetryAfter is the longest Retry-After the client waits out. A longer
// one ends the read at once, rather than leaving a command hanging.
const maxRetryAfter = time.Minute

// StatusError is an answer other than success, such as 403 for a token
// without the permission to read something.
type StatusError struct {
	// Path is the request's path, such as /api/dcim/device-types/.
	Path       string
	StatusCode int
	// Message is the answer's explanation, from its detail or error field, or
	// its text, cut short. It comes from the server, so print it quoted, as
	// Error does.
	Message string
	// Location is where a redirect pointed, cut short.
	Location string
	// APIVersion is the answer's API-Version header, cut short, or empty.
	APIVersion string
}

func (e *StatusError) Error() string {
	who := "NetBox"
	if !e.FromNetBox() {
		who = "the server"
	}
	msg := fmt.Sprintf("GET %s: %s answered %d %s", e.Path, who, e.StatusCode, http.StatusText(e.StatusCode))
	if e.StatusCode >= 300 && e.StatusCode < 400 {
		msg += fmt.Sprintf(", a redirect to %q, which TrueUp doesn't follow: set netbox.url to the address NetBox's API answers on", e.Location)
	}
	if e.Message != "" {
		msg += fmt.Sprintf(": %q", e.Message)
	}
	if !e.FromNetBox() && e.StatusCode >= 400 {
		msg += "; the answer has no API-Version header, so a proxy or firewall in front of NetBox may have given it"
	}
	return msg
}

// FromNetBox reports whether NetBox itself gave the answer. Its middleware
// sets API-Version on every API answer, errors included (CoreMiddleware in
// netbox/netbox/middleware.py), and a proxy or firewall in front of it
// doesn't.
func (e *StatusError) FromNetBox() bool {
	return e.APIVersion != ""
}

// refusedByNetBox reports whether err is NetBox itself answering 403, for a
// token it refuses or a permission the token's user lacks, rather than a
// proxy or firewall in front of it.
func refusedByNetBox(err error) bool {
	se, ok := errors.AsType[*StatusError](err)
	return ok && se.StatusCode == http.StatusForbidden && se.FromNetBox()
}

// errTooLarge marks an answer over the size limit.
var errTooLarge = errors.New("NetBox's answer is over the size limit")

// answer is NetBox's whole answer to one request.
type answer struct {
	status int
	header http.Header
	body   []byte
}

// getJSON sends a GET request for path, with query, and decodes NetBox's JSON
// answer into out. It retries, up to c.retries times, a read that failed in a
// way a later attempt might not (see transient and retryableStatus), and
// returns the answer's header. It is the only way the client sends a
// request, and it only ever sends GET.
func (c *Client) getJSON(ctx context.Context, path string, query url.Values, out any) (http.Header, error) {
	target := c.endpoint(path, query)
	for retry := 0; ; retry++ {
		ans, err := c.send(ctx, path, query, target, retry+1)
		if err == nil && !retryableStatus(ans.status) {
			return ans.header, c.decode(path, ans, out)
		}
		if err == nil {
			err = c.statusError(path, ans)
		} else if !transient(err) {
			return nil, err
		}
		if ctx.Err() != nil {
			if !errors.Is(err, ctx.Err()) {
				err = fmt.Errorf("%w; interrupted: %w", err, ctx.Err())
			}
			return nil, err
		}
		if retry >= c.retries {
			if retry > 0 {
				err = fmt.Errorf("%w (tried %d times)", err, retry+1)
			}
			return nil, err
		}
		wait, waitErr := c.retryWait(ctx, retry+1, ans, err)
		if waitErr != nil {
			return nil, waitErr
		}
		c.log.Warn("retrying a NetBox request", "path", path, "retry", retry+1, "retries", c.retries, "after", wait, "error", err.Error())
		if sleepErr := c.sleep(ctx, wait); sleepErr != nil {
			return nil, fmt.Errorf("%w; interrupted while waiting to try again: %w", err, sleepErr)
		}
	}
}

// endpoint returns the URL for an API path, such as /api/status/, under
// NetBox's base URL.
func (c *Client) endpoint(path string, query url.Values) string {
	u := *c.base
	u.Path = c.base.Path + path
	u.RawQuery = query.Encode()
	return u.String()
}

// send makes one attempt at a GET request for target, and reads the whole
// answer. It first waits for a free slot, so that no more than MaxRequests
// are in flight, and gives the attempt c.timeout. An error means no whole
// answer arrived.
func (c *Client) send(ctx context.Context, path string, query url.Values, target string, attempt int) (ans answer, err error) {
	select {
	case c.slots <- struct{}{}:
	case <-ctx.Done():
		return answer{}, fmt.Errorf("GET %s: %w", path, ctx.Err())
	}
	defer func() { <-c.slots }()

	attemptCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(attemptCtx, http.MethodGet, target, nil)
	if err != nil {
		return answer{}, fmt.Errorf("GET %s: %w", path, err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	// security: the only place the token leaves the client. The log below
	// gives the scheme and "[redacted]" instead.
	req.Header.Set("Authorization", c.scheme+" "+c.token.Reveal())

	start := time.Now()
	defer func() {
		if !c.log.Enabled(ctx, slog.LevelDebug) {
			return
		}
		attrs := []any{
			"method", http.MethodGet, "path", path, "query", cut(query.Encode()),
			"authorization", c.scheme + " [redacted]", "attempt", attempt, "duration", time.Since(start),
		}
		if err != nil {
			attrs = append(attrs, "error", err.Error())
		} else {
			attrs = append(attrs, "status", ans.status, "bytes", len(ans.body),
				"request_id", cut(c.redact(ans.header.Get("X-Request-ID"))))
		}
		c.log.Debug("NetBox request", attrs...)
	}()

	resp, err := c.http.Do(req)
	if err != nil {
		return answer{}, c.requestError(ctx, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := c.readBody(resp)
	if err != nil {
		return answer{}, c.requestError(ctx, path, err)
	}
	return answer{status: resp.StatusCode, header: resp.Header, body: body}, nil
}

// maxErrorBody is how much of an answer other than success the client reads.
// Its message is cut much shorter anyway, so a large error page, from a proxy
// say, is still reported, and retried, by its status.
const maxErrorBody = 64 << 10

// readBody reads an answer's body, up to the size limit. Of an answer other
// than success it reads at most maxErrorBody, and drops the rest.
func (c *Client) readBody(resp *http.Response) ([]byte, error) {
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, err := io.ReadAll(io.LimitReader(resp.Body, min(c.maxResponse, maxErrorBody)))
		if err != nil {
			return nil, fmt.Errorf("read the answer: %w", err)
		}
		return body, nil
	}
	if resp.ContentLength > c.maxResponse {
		return nil, fmt.Errorf("%w of %d bytes (netbox.max_response_size): it is %d bytes", errTooLarge, c.maxResponse, resp.ContentLength)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, c.maxResponse+1))
	if err != nil {
		return nil, fmt.Errorf("read the answer: %w", err)
	}
	if int64(len(body)) > c.maxResponse {
		return nil, fmt.Errorf("%w of %d bytes (netbox.max_response_size)", errTooLarge, c.maxResponse)
	}
	return body, nil
}

// requestError explains why an attempt got no whole answer. ctx is the
// caller's, so a deadline that passed while it is still alive was the
// attempt's own timeout.
//
// The error's text can hold what the server sent, such as an HTTP/2 GOAWAY's
// debug data or a certificate's names, so the token is removed from it too.
func (c *Client) requestError(ctx context.Context, path string, err error) error {
	if urlErr, ok := errors.AsType[*url.Error](err); ok {
		err = urlErr.Err // without the URL, which errors already give as path
	}
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		err = fmt.Errorf("GET %s: NetBox didn't answer within %s (netbox.timeout): %w", path, c.timeout, err)
	} else {
		err = fmt.Errorf("GET %s: %w", path, err)
	}
	return redactedError{err: err, text: c.redact(err.Error())}
}

// redactedError is err with the token removed from its text.
type redactedError struct {
	err  error
	text string
}

func (e redactedError) Error() string { return e.text }
func (e redactedError) Unwrap() error { return e.err }

// transient reports whether a request that got no answer might get one if
// tried again. It is false for what would only fail again: a certificate that
// doesn't verify, a TLS handshake the server refuses, plain http where https
// was asked for, a host name that doesn't exist, and an answer over the size
// limit.
func transient(err error) bool {
	// Over TCP, crypto/tls gives an alert from the server, such as for a
	// client certificate TrueUp doesn't have, as a "remote error"
	// (crypto/tls/conn.go). tls.AlertError is only for QUIC.
	if opErr, ok := errors.AsType[*net.OpError](err); ok && opErr.Op == "remote error" {
		return false
	}
	if errors.Is(err, errTooLarge) || errors.Is(err, http.ErrSchemeMismatch) {
		return false
	}
	if _, ok := errors.AsType[*tls.CertificateVerificationError](err); ok {
		return false
	}
	if _, ok := errors.AsType[x509.UnknownAuthorityError](err); ok {
		return false
	}
	if _, ok := errors.AsType[x509.HostnameError](err); ok {
		return false
	}
	if _, ok := errors.AsType[x509.CertificateInvalidError](err); ok {
		return false
	}
	if _, ok := errors.AsType[tls.RecordHeaderError](err); ok {
		return false
	}
	if dnsErr, ok := errors.AsType[*net.DNSError](err); ok {
		return !dnsErr.IsNotFound
	}
	return true
}

// retryableStatus reports whether a later attempt might get a better answer
// than code: too many requests, or a server or gateway error. NetBox's own
// 500s are often bugs that repeat, but a database that is briefly
// unavailable gives 500 too.
func retryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

// retryWait returns how long to wait before the given retry, counting from
// 1: what NetBox asked for with Retry-After on 429 or 503, or else the
// backoff. Instead it returns failure, explained, if NetBox asked for longer
// than maxRetryAfter, or if the wait would end after ctx's deadline.
func (c *Client) retryWait(ctx context.Context, retry int, ans answer, failure error) (time.Duration, error) {
	wait := c.backoff.delay(retry)
	if ans.status == http.StatusTooManyRequests || ans.status == http.StatusServiceUnavailable {
		if after, ok := retryAfter(ans.header.Get("Retry-After"), c.now()); ok {
			if after > maxRetryAfter {
				return 0, fmt.Errorf("%w; NetBox asked to wait %s before trying again, longer than TrueUp waits (%s)", failure, after, maxRetryAfter)
			}
			wait = after
		}
	}
	if deadline, ok := ctx.Deadline(); ok && c.now().Add(wait).After(deadline) {
		return 0, fmt.Errorf("%w; no time is left to try again", failure)
	}
	return wait, nil
}

// retryAfter reads a Retry-After header: a number of seconds, or an HTTP date
// (RFC 9110, section 10.2.3). A date in the past means now. A number too
// large to hold is read as 2^32 seconds, which is still far too long to wait.
func retryAfter(v string, now time.Time) (time.Duration, bool) {
	const most = 1 << 32 // seconds, which fit in a time.Duration
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	secs, err := strconv.ParseUint(v, 10, 64)
	if errors.Is(err, strconv.ErrRange) {
		secs, err = most, nil
	}
	if err == nil {
		return time.Duration(min(secs, most)) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(t.Sub(now), 0), true
	}
	return 0, false
}

// backoff is how long to wait before each retry, without a Retry-After.
type backoff struct {
	base, max time.Duration
	exact     bool // without jitter, for tests
}

var defaultBackoff = backoff{base: time.Second, max: 30 * time.Second}

// delay returns the wait before the given retry, counting from 1: base,
// doubled for each retry up to max, with jitter that keeps between half and
// all of it, so that clients that failed together don't retry together.
func (b backoff) delay(retry int) time.Duration {
	d := b.max
	if shift := retry - 1; shift < 30 && b.base<<shift < b.max {
		d = b.base << shift
	}
	if b.exact {
		return d
	}
	return d/2 + rand.N(d/2+1) //nolint:gosec // G404: jitter needn't be unpredictable
}

// sleep waits for d, or until ctx ends.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// decode checks a final answer and decodes its JSON into out. Any status
// but success is a *StatusError. A successful answer must be JSON, and once
// connected, must carry the API version that Connect saw.
func (c *Client) decode(path string, ans answer, out any) error {
	if ans.status < 200 || ans.status > 299 {
		return c.statusError(path, ans)
	}
	if ct := ans.header.Get("Content-Type"); !isJSON(ct) {
		return fmt.Errorf("GET %s: NetBox answered with %q, not JSON: is netbox.url the address of NetBox itself?", path, cut(c.redact(ct)))
	}
	if c.apiVersion != "" {
		if got := ans.header.Get("API-Version"); got != c.apiVersion {
			return fmt.Errorf("GET %s: NetBox answered with API-Version %q, not %s: was it upgraded while TrueUp was running?", path, cut(c.redact(got)), c.apiVersion)
		}
	}
	if err := json.Unmarshal(ans.body, out); err != nil {
		return fmt.Errorf("GET %s: decode the answer: %w", path, err)
	}
	return nil
}

func isJSON(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	return err == nil && mediaType == "application/json"
}

func (c *Client) statusError(path string, ans answer) *StatusError {
	e := &StatusError{
		Path: path, StatusCode: ans.status, Message: cut(c.redact(message(ans))),
		APIVersion: cut(c.redact(ans.header.Get("API-Version"))),
	}
	if ans.status >= 300 && ans.status < 400 {
		e.Location = cut(c.redact(ans.header.Get("Location")))
	}
	return e
}

// message returns NetBox's explanation of an answer that isn't success: the
// detail field REST framework gives its errors, the error field of NetBox's
// own handler for server errors (handle_rest_api_exception in
// netbox/utilities/error_handlers.py), or otherwise the text of the answer.
// An HTML page, such as a proxy's error page, gives nothing.
func message(ans answer) string {
	ct := ans.header.Get("Content-Type")
	if isJSON(ct) {
		var e struct {
			Detail *string `json:"detail"`
			Error  *string `json:"error"`
		}
		if err := json.Unmarshal(ans.body, &e); err == nil {
			switch {
			case e.Detail != nil:
				return *e.Detail
			case e.Error != nil:
				return *e.Error
			}
		}
		return string(ans.body) // such as a validation error for each filter
	}
	if mediaType, _, err := mime.ParseMediaType(ct); err == nil && mediaType == "text/plain" {
		return string(ans.body)
	}
	return ""
}

// redact removes the token from text that came from NetBox, in case a NetBox,
// or something posing as one, repeats it back: the whole token, its secret
// part, and either as JSON would escape it. A secret part too short to be one
// NetBox made is left alone, rather than turning every letter it matches into
// "[redacted]".
func (c *Client) redact(s string) string {
	const minSecret = 8 // NetBox makes secrets of 40 characters (TOKEN_DEFAULT_LENGTH)
	t := c.token.Reveal()
	parts := []string{t}
	if c.tokenVersion == 2 {
		if _, secret, ok := strings.Cut(t, "."); ok && len(secret) >= minSecret {
			parts = append(parts, secret)
		}
	}
	for _, part := range parts {
		s = strings.ReplaceAll(s, part, "[redacted]")
		if quoted, err := json.Marshal(part); err == nil {
			s = strings.ReplaceAll(s, string(quoted[1:len(quoted)-1]), "[redacted]")
		}
	}
	return s
}

// cut shortens text from NetBox for a message or a log. It can hold
// anything, so print it quoted.
func cut(s string) string {
	const limit = 300
	s = strings.TrimSpace(s)
	if len(s) > limit {
		s = strings.ToValidUTF8(s[:limit], "") + "…"
	}
	return s
}
