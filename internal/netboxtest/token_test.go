package netboxtest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// Fake values for a fake NetBox. Errors must never repeat them.
const (
	testUser     = "admin"
	testPassword = "insecure-test-only"
	testKey      = "keykeykeykey"
	testSecret   = "secretsecretsecretsecretsecretsecret0000"
)

// fakeNetBox answers the token provisioning endpoint with status and body,
// and records the request it got.
func fakeNetBox(t *testing.T, status int, body string) (*httptest.Server, *recorded) {
	t.Helper()
	rec := &recorded{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.hits.Add(1)
		rec.method, rec.path, rec.contentType, rec.accept = r.Method, r.URL.Path, r.Header.Get("Content-Type"), r.Header.Get("Accept")
		if err := json.NewDecoder(r.Body).Decode(&rec.body); err != nil {
			t.Errorf("fake NetBox: decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

type recorded struct {
	hits                              atomic.Int32
	method, path, contentType, accept string
	body                              map[string]any
}

func TestProvisionToken(t *testing.T) {
	t.Parallel()
	srv, rec := fakeNetBox(t, http.StatusCreated, fmt.Sprintf(
		`{"id": 1, "version": 2, "key": %q, "token": %q, "write_enabled": true, "user": {"username": "admin"}}`,
		testKey, testSecret))

	tok, err := provisionToken(t.Context(), srv.URL, testUser, testPassword, "a test")
	if err != nil {
		t.Fatalf("provisionToken: %v", err)
	}
	if got, want := tok.Reveal(), "nbt_"+testKey+"."+testSecret; got != want {
		t.Errorf("token = %q, want %q", got, want)
	}
	if got := fmt.Sprint(tok); strings.Contains(got, testSecret) {
		t.Errorf("token prints as %q, which holds the secret", got)
	}

	if rec.method != http.MethodPost || rec.path != "/api/users/tokens/provision/" {
		t.Errorf("request = %s %s, want POST /api/users/tokens/provision/", rec.method, rec.path)
	}
	if rec.contentType != "application/json" || rec.accept != "application/json" {
		t.Errorf("Content-Type = %q, Accept = %q; want application/json for both", rec.contentType, rec.accept)
	}
	wantBody := map[string]any{"username": testUser, "password": testPassword, "description": "a test", "write_enabled": true}
	if diff := cmp.Diff(wantBody, rec.body); diff != "" {
		t.Errorf("request body (-want +got):\n%s", diff)
	}
}

func TestProvisionTokenFails(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{
			name:   "wrong password",
			status: http.StatusForbidden,
			body:   `{"detail": "Invalid username/password"}`,
			want:   `NetBox answered 403, not 201: "{\"detail\": \"Invalid username/password\"}"`,
		},
		{
			name:   "server error with an empty body",
			status: http.StatusInternalServerError,
			want:   `NetBox answered 500, not 201: ""`,
		},
		{
			name:   "control characters in the body",
			status: http.StatusBadGateway,
			body:   "\x1b[31mbad gateway",
			want:   `NetBox answered 502, not 201: "\x1b[31mbad gateway"`,
		},
		{
			name:   "v1 token",
			status: http.StatusCreated,
			body:   fmt.Sprintf(`{"version": 1, "key": null, "token": %q}`, testSecret),
			want:   "NetBox made a v1 token, not v2; check API_TOKEN_PEPPERS",
		},
		{
			name:   "no token in the answer",
			status: http.StatusCreated,
			body:   fmt.Sprintf(`{"version": 2, "key": %q}`, testKey),
			want:   "NetBox's answer has no usable key and token",
		},
		{
			name:   "key with characters NetBox doesn't use",
			status: http.StatusCreated,
			body:   fmt.Sprintf(`{"version": 2, "key": "AbCd.Ef0123", "token": %q}`, testSecret),
			want:   "NetBox's answer has no usable key and token",
		},
		{
			name:   "answer that isn't JSON",
			status: http.StatusCreated,
			body:   `<html>NetBox</html>`,
			want:   "decode NetBox's answer",
		},
		{
			name:   "answer over the size limit",
			status: http.StatusCreated,
			body:   `{"description": "` + strings.Repeat("x", maxResponse) + `"}`,
			want:   "NetBox's answer is over 1048576 bytes",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv, _ := fakeNetBox(t, tt.status, tt.body)
			_, err := provisionToken(t.Context(), srv.URL, testUser, testPassword, "a test")
			if err == nil {
				t.Fatalf("provisionToken succeeded, want an error containing %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to contain %q", err, tt.want)
			}
			for _, secret := range []string{testPassword, testSecret} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("error %q repeats a secret", err)
				}
			}
		})
	}
}

func TestProvisionTokenRefusesURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		url  string
		want string
	}{
		{"http://netbox.example.com:8000", "isn't on this machine"},
		{"http://192.0.2.10:8000", "isn't on this machine"},
		{"http://[2001:db8::1]:8000", "isn't on this machine"},
		{"https://127.0.0.1:8000", "must be http://HOST:PORT"},
		{"http://admin:pw@127.0.0.1:8000", "must be http://HOST:PORT"},
		{"http://127.0.0.1:8000/netbox", "must be http://HOST:PORT"},
		{"http://127.0.0.1:8000/?next=/", "must be http://HOST:PORT"},
		{"127.0.0.1:8000", "NetBox URL"},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			t.Parallel()
			_, err := provisionToken(t.Context(), tt.url, testUser, testPassword, "a test")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("provisionToken(%q) error = %v, want it to contain %q", tt.url, err, tt.want)
			}
		})
	}

	for _, ok := range []string{"http://127.0.0.1:8000", "http://localhost:8000/", "http://[::1]:8000"} {
		if _, err := localEndpoint(ok, "/api/"); err != nil {
			t.Errorf("localEndpoint(%q): %v", ok, err)
		}
	}
}

// A redirect could carry the password elsewhere, so it is an error, not a
// request to follow.
func TestProvisionTokenDoesNotFollowRedirects(t *testing.T) {
	t.Parallel()
	elsewhere, rec := fakeNetBox(t, http.StatusCreated, `{}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", elsewhere.URL+"/api/users/tokens/provision/")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	t.Cleanup(srv.Close)

	_, err := provisionToken(t.Context(), srv.URL, testUser, testPassword, "a test")
	if err == nil || !strings.Contains(err.Error(), "NetBox answered 307") {
		t.Errorf("provisionToken error = %v, want NetBox answered 307", err)
	}
	if n := rec.hits.Load(); n != 0 {
		t.Errorf("the redirect was followed %d times", n)
	}
}

func TestProvisionTokenCanceled(t *testing.T) {
	t.Parallel()
	srv, _ := fakeNetBox(t, http.StatusCreated, `{}`)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := provisionToken(ctx, srv.URL, testUser, testPassword, "a test"); err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("provisionToken error = %v, want context canceled", err)
	}
}
