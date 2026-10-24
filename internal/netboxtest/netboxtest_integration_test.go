//go:build integration

package netboxtest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// nb is the NetBox that TestMain starts for every test in the package.
var nb *NetBox

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	// Longer than the 15m45s compose.yaml's health checks allow, so that
	// they, not this, report a NetBox that never becomes ready.
	ctx, cancel := context.WithTimeoutCause(context.Background(), 25*time.Minute,
		errors.New("NetBox didn't start within 25 minutes"))
	defer cancel()
	var err error
	nb, err = Start(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "start NetBox:", err)
		if cause := context.Cause(ctx); cause != nil {
			fmt.Fprintln(os.Stderr, "Cause:", cause)
		}
		return 1
	}

	code := m.Run()

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer stopCancel()
	if err := nb.Stop(stopCtx); err != nil {
		fmt.Fprintln(os.Stderr, "stop NetBox:", err)
		if code == 0 {
			code = 1
		}
	}
	return code
}

// TestStatusAndToken is the smoke test: NetBox reports the target version,
// and only the provisioned v2 token gets in. LOGIN_REQUIRED is on in
// netbox-docker's image, so /api/status/ refuses a request that isn't
// authenticated, and a 200 proves the token was accepted.
func TestStatusAndToken(t *testing.T) {
	t.Parallel()
	token := nb.Token.Reveal()
	key, _, ok := strings.Cut(strings.TrimPrefix(token, "nbt_"), ".")
	if !strings.HasPrefix(token, "nbt_") || !ok {
		t.Fatalf("token isn't in the v2 form nbt_<key>.<token>")
	}

	tests := []struct {
		name          string
		authorization string
		want          int
	}{
		{name: "v2 token", authorization: "Bearer " + token, want: http.StatusOK},
		{name: "no token", want: http.StatusForbidden},
		{name: "right key, wrong secret", authorization: "Bearer nbt_" + key + "." + strings.Repeat("0", 40), want: http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			status, body := getStatus(t, tt.authorization)
			if status != tt.want {
				t.Fatalf("GET /api/status/ = %d, want %d: %q", status, tt.want, snippet(body))
			}
			if status != http.StatusOK {
				return
			}
			var st struct {
				Version string `json:"netbox-version"`
			}
			if err := json.Unmarshal(body, &st); err != nil {
				t.Fatalf("decode /api/status/: %v", err)
			}
			if st.Version != Version {
				t.Errorf("NetBox reports version %q, want %q", st.Version, Version)
			}
		})
	}
}

// TestStartReportsFailure starts a stack whose only service dies at once.
// The error must say which service failed and end with its log, and start
// must remove what it started without an error of its own.
func TestStartReportsFailure(t *testing.T) {
	t.Parallel()
	st, _, err := parseStack(composeFile)
	if err != nil {
		t.Fatal(err)
	}
	broken := fmt.Sprintf(`
services:
  netbox:
    image: %s
    command: ["sh", "-c", "echo netboxtest-was-here; exit 3"]
    environment:
      SUPERUSER_NAME: admin
      SUPERUSER_PASSWORD: insecure-test-only
    ports: ["127.0.0.1:0:6379"]
    healthcheck:
      test: ["CMD", "true"]
      start_period: 1m
      start_interval: 1s
      interval: 30s
      timeout: 5s
      retries: 3
`, st.Services["valkey"].Image)

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	got, err := start(ctx, []byte(broken))
	if err == nil {
		_ = got.Stop(ctx)
		t.Fatal("start succeeded with a service that exits")
	}
	for _, want := range []string{"start netbox", "container exited with code 3", "The end of its log:", "netboxtest-was-here"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("start error doesn't contain %q:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), "clean up after a failed start") {
		t.Errorf("start couldn't clean up after itself:\n%v", err)
	}
}

func getStatus(t *testing.T, authorization string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, nb.URL+"/api/status/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "application/json")
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	status, body, err := do(req)
	if err != nil {
		t.Fatalf("GET /api/status/: %v", err)
	}
	return status, body
}
