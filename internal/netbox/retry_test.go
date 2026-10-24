package netbox

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

// failing makes f answer the first n status requests with status and the
// given headers, and then as usual.
func failing(f *fakeNetBox, n int64, status int, header http.Header) {
	var count atomic.Int64
	f.handle = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != statusPath || count.Add(1) > n {
			return false
		}
		for k, v := range header {
			w.Header()[k] = v
		}
		f.writeJSON(w, status, map[string]any{"detail": "try later"})
		return true
	}
}

// noJitter makes the backoff wait its whole length.
var noJitter = backoff{base: time.Second, max: 30 * time.Second, exact: true}

func TestRetries(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		status    int
		failures  int64
		header    http.Header
		retries   int
		wantErr   string // a substring, or "" for success
		wantTries int    // status requests
		wantWaits int    // waits recorded
		wantWait  time.Duration
	}{
		{name: "503 then success", status: 503, failures: 1, retries: 3, wantTries: 2, wantWaits: 1},
		{name: "429 then success", status: 429, failures: 2, retries: 3, wantTries: 3, wantWaits: 2},
		{name: "500, 502 and 504 are retried", status: 502, failures: 3, retries: 3, wantTries: 4, wantWaits: 3},
		{
			name: "every try fails", status: 500, failures: 9, retries: 3,
			wantErr: `NetBox answered 500 Internal Server Error: "try later" (tried 4 times)`, wantTries: 4, wantWaits: 3,
		},
		{name: "no retries", status: 503, failures: 9, retries: 0, wantErr: "NetBox answered 503", wantTries: 1},
		{name: "404 isn't retried", status: 404, failures: 9, retries: 3, wantErr: "NetBox answered 404", wantTries: 1},
		{name: "400 isn't retried", status: 400, failures: 9, retries: 3, wantErr: "NetBox answered 400", wantTries: 1},
		{
			name: "Retry-After in seconds", status: 429, failures: 1, retries: 3, header: http.Header{"Retry-After": {"7"}},
			wantTries: 2, wantWaits: 1, wantWait: 7 * time.Second,
		},
		{
			name: "Retry-After as a date", status: 503, failures: 1, retries: 3,
			header:    http.Header{"Retry-After": {now.Add(10 * time.Second).Format(http.TimeFormat)}},
			wantTries: 2, wantWaits: 1, wantWait: 10 * time.Second,
		},
		{
			name: "Retry-After on a 502 is ignored", status: 502, failures: 1, retries: 3, header: http.Header{"Retry-After": {"7"}},
			wantTries: 2, wantWaits: 1, wantWait: time.Second,
		},
		{
			name: "Retry-After past counting", status: 429, failures: 1, retries: 3, header: http.Header{"Retry-After": {"99999999999"}},
			wantErr: "longer than TrueUp waits", wantTries: 1,
		},
		{
			name: "Retry-After too long", status: 503, failures: 1, retries: 3, header: http.Header{"Retry-After": {"120"}},
			wantErr:   `NetBox answered 503 Service Unavailable: "try later"; NetBox asked to wait 2m0s before trying again, longer than TrueUp waits (1m0s)`,
			wantTries: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFake(t)
			failing(f, tt.failures, tt.status, tt.header)
			c := newTestClient(t, f, Options{Retries: tt.retries}, func(c *Client) {
				c.backoff = noJitter
				c.now = func() time.Time { return now }
			})
			err := c.connect(t.Context())
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("connect: %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("connect error = %v, want one containing %q", err, tt.wantErr)
			}
			tries := 0
			for _, p := range f.paths() {
				if p == statusPath {
					tries++
				}
			}
			if tries != tt.wantTries {
				t.Errorf("status requested %d times, want %d", tries, tt.wantTries)
			}
			if len(*c.sleeps) != tt.wantWaits {
				t.Fatalf("waited %v, want %d waits", *c.sleeps, tt.wantWaits)
			}
			if tt.wantWait != 0 && (*c.sleeps)[0] != tt.wantWait {
				t.Errorf("waited %v first, want %v", (*c.sleeps)[0], tt.wantWait)
			}
			if tt.wantWaits > 0 && !strings.Contains(c.logs.String(), `"msg":"retrying a NetBox request"`) {
				t.Errorf("retries weren't logged:\n%s", c.logs)
			}
		})
	}
}

// Waits double from 1s, with jitter.
func TestRetriesBackOff(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	failing(f, 9, http.StatusServiceUnavailable, nil)
	c := newTestClient(t, f, Options{Retries: 6}, func(c *Client) { c.backoff = noJitter })
	if err := c.connect(t.Context()); err == nil {
		t.Fatal("connect succeeded, though NetBox kept answering 503")
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second}
	if diff := cmp.Diff(want, *c.sleeps); diff != "" {
		t.Errorf("waits (-want +got):\n%s", diff)
	}
}

func TestBackoffDelay(t *testing.T) {
	t.Parallel()

	tests := []struct {
		retry    int
		min, max time.Duration
	}{
		{retry: 1, min: 500 * time.Millisecond, max: time.Second},
		{retry: 2, min: time.Second, max: 2 * time.Second},
		{retry: 3, min: 2 * time.Second, max: 4 * time.Second},
		{retry: 6, min: 15 * time.Second, max: 30 * time.Second},
		{retry: 100, min: 15 * time.Second, max: 30 * time.Second},
	}
	for _, tt := range tests {
		for range 50 {
			if d := defaultBackoff.delay(tt.retry); d < tt.min || d > tt.max {
				t.Errorf("delay(%d) = %v, want %v to %v", tt.retry, d, tt.min, tt.max)
			}
		}
	}
}

func TestRetryAfter(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		header string
		want   time.Duration
		ok     bool
	}{
		{header: "5", want: 5 * time.Second, ok: true},
		{header: " 5 ", want: 5 * time.Second, ok: true},
		{header: "0", ok: true},
		{header: now.Add(90 * time.Second).Format(http.TimeFormat), want: 90 * time.Second, ok: true},
		{header: now.Add(-time.Hour).Format(http.TimeFormat), ok: true},
		{header: ""},
		{header: "-1"},
		{header: "1.5"},
		{header: "soon"},
		{header: "99999999999", want: 1 << 32 * time.Second, ok: true},
		{header: "999999999999999999999999999999", want: 1 << 32 * time.Second, ok: true},
	}
	for _, tt := range tests {
		if got, ok := retryAfter(tt.header, now); got != tt.want || ok != tt.ok {
			t.Errorf("retryAfter(%q) = %v, %v; want %v, %v", tt.header, got, ok, tt.want, tt.ok)
		}
	}
}

func TestTransient(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "connection refused", err: &net.OpError{Op: "dial", Err: errors.New("connection refused")}, want: true},
		{name: "a timeout", err: fmt.Errorf("GET /api/: %w", context.DeadlineExceeded), want: true},
		{name: "a lookup that may work later", err: &net.DNSError{Err: "server misbehaving", IsTemporary: true}, want: true},
		{name: "an unknown host", err: &net.DNSError{Err: "no such host", IsNotFound: true}},
		{name: "a certificate that doesn't verify", err: &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}},
		{name: "a certificate for another host", err: fmt.Errorf("x: %w", x509.HostnameError{Host: "netbox.example.com"})},
		{name: "plain http", err: http.ErrSchemeMismatch},
		{name: "not TLS", err: tls.RecordHeaderError{Msg: "first record does not look like a TLS handshake"}},
		{name: "a handshake the server refuses", err: &net.OpError{Op: "remote error", Err: errors.New("tls: handshake failure")}},
		{name: "a connection reset", err: &net.OpError{Op: "read", Err: errors.New("connection reset by peer")}, want: true},
		{name: "an answer over the limit", err: fmt.Errorf("GET /api/: %w", errTooLarge)},
	}
	for _, tt := range tests {
		if got := transient(tt.err); got != tt.want {
			t.Errorf("transient(%s) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// A request that takes longer than the timeout is tried again.
func TestTimeoutsAreRetried(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	var count atomic.Int32
	f.handle = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == statusPath && count.Add(1) == 1 {
			<-r.Context().Done() // never answer: the client gives up first
			return true
		}
		return false
	}
	c := newTestClient(t, f, Options{Timeout: 100 * time.Millisecond, Retries: 1}, nil)
	if err := c.connect(t.Context()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if !strings.Contains(c.logs.String(), "NetBox didn't answer within 100ms (netbox.timeout)") {
		t.Errorf("the timeout wasn't logged:\n%s", c.logs)
	}
}

// A NetBox that can't be reached is retried, then reported.
func TestUnreachableNetBox(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.srv.Close()
	c := newTestClient(t, f, Options{Retries: 2}, nil)
	err := c.connect(t.Context())
	if err == nil || !strings.Contains(err.Error(), "read NetBox's status: GET /api/status/:") || !strings.Contains(err.Error(), "(tried 3 times)") {
		t.Errorf("connect error = %v, want one for the status after 3 tries", err)
	}
	if len(*c.sleeps) != 2 {
		t.Errorf("waited %v, want 2 waits", *c.sleeps)
	}
}

// No retry waits past the context's deadline.
func TestRetriesStopAtTheDeadline(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	failing(f, 9, http.StatusServiceUnavailable, http.Header{"Retry-After": {"30"}})
	c := newTestClient(t, f, Options{Retries: 3}, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	err := c.connect(ctx)
	if err == nil || !strings.Contains(err.Error(), "no time is left to try again") {
		t.Errorf("connect error = %v, want one saying no time is left", err)
	}
	if len(*c.sleeps) != 0 {
		t.Errorf("waited %v, past the deadline", *c.sleeps)
	}
}

// A cancelled context ends a wait between retries.
func TestCancelStopsRetries(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	failing(f, 9, http.StatusServiceUnavailable, nil)
	ctx, cancel := context.WithCancel(t.Context())
	c := newTestClient(t, f, Options{Retries: 3}, func(c *Client) {
		c.sleep = func(ctx context.Context, d time.Duration) error {
			cancel()
			return sleep(ctx, d)
		}
	})
	if err := c.connect(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("connect error = %v, want context.Canceled", err)
	}
	if got := len(f.paths()); got != 1 {
		t.Errorf("made %d requests, want 1", got)
	}
}

// No more than MaxRequests requests are in flight at once.
func TestMaxRequests(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.add("/api/dcim/manufacturers/", ids(1), nil)
	var inFlight, most atomic.Int32
	c := connected(t, f, Options{MaxRequests: 3}, nil)
	f.mu.Lock()
	f.handle = func(w http.ResponseWriter, r *http.Request) bool {
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			m := most.Load()
			if n <= m || most.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		return false
	}
	f.mu.Unlock()

	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			if _, err := c.CanRead(t.Context(), "/api/dcim/manufacturers/"); err != nil {
				t.Errorf("CanRead: %v", err)
			}
		})
	}
	wg.Wait()
	if got := most.Load(); got != 3 {
		t.Errorf("at most %d requests were in flight, want 3", got)
	}
}

// A server that refuses the handshake, here for want of a client
// certificate, isn't asked again, whichever TLS version it speaks.
func TestRefusedHandshakesAreNotRetried(t *testing.T) {
	t.Parallel()
	for _, version := range []uint16{tls.VersionTLS12, tls.VersionTLS13} {
		t.Run(tls.VersionName(version), func(t *testing.T) {
			t.Parallel()
			f := newFake(t)
			srv := httptest.NewUnstartedServer(http.HandlerFunc(f.serve))
			srv.Config.ErrorLog = log.New(io.Discard, "", 0)
			srv.TLS = &tls.Config{ClientAuth: tls.RequireAnyClientCert, MinVersion: version, MaxVersion: version}
			srv.StartTLS()
			t.Cleanup(srv.Close)

			c := newTestClient(t, f, Options{URL: srv.URL, Retries: 3, CACerts: []*x509.Certificate{srv.Certificate()}}, nil)
			err := c.connect(t.Context())
			if err == nil || !strings.Contains(err.Error(), "remote error: tls:") {
				t.Errorf("connect error = %v, want a remote TLS error", err)
			}
			if len(*c.sleeps) != 0 {
				t.Errorf("a refused handshake was retried after %v", *c.sleeps)
			}
		})
	}
}

// An error page over the size limit is still an answer with a status, and
// retried by it.
func TestLargeErrorPagesAreRetried(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	var count atomic.Int64
	f.handle = func(w http.ResponseWriter, r *http.Request) bool {
		if count.Add(1) > 1 {
			return false
		}
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(strings.Repeat("<p>Bad gateway</p>", 1000)))
		return true
	}
	c := newTestClient(t, f, Options{Retries: 1, MaxResponseSize: 1024}, nil)
	if err := c.connect(t.Context()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if len(*c.sleeps) != 1 {
		t.Errorf("waited %v, want one retry", *c.sleeps)
	}
}

// Errors from the transport can hold what the server sent, and lose the
// token too.
func TestRequestErrorsAreRedacted(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	c := newTestClient(t, f, Options{}, nil)
	cause := &net.OpError{Op: "read", Err: errors.New("GOAWAY debug data: Bearer " + testToken)}
	err := c.requestError(t.Context(), statusPath, cause)
	if strings.Contains(err.Error(), testSecret) || !strings.Contains(err.Error(), "Bearer [redacted]") {
		t.Errorf("requestError = %q, want the token redacted", err)
	}
	if !errors.Is(err, cause) {
		t.Error("requestError lost the error it wraps")
	}
}
