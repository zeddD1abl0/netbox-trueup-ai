package netboxtest

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
)

func TestSchema(t *testing.T) {
	t.Parallel()
	var got *http.Request
	schema := `{"openapi": "3.0.3"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		w.Header().Set("Content-Type", "application/vnd.oai.openapi+json")
		_, _ = w.Write([]byte(schema))
	}))
	t.Cleanup(srv.Close)
	nb := &NetBox{URL: srv.URL, Token: config.NewSecret(testToken)}
	body, err := nb.Schema(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != schema {
		t.Errorf("Schema = %q, want %q", body, schema)
	}
	if got.Method != http.MethodGet || got.URL.Path != "/api/schema/" || got.URL.RawQuery != "format=json" {
		t.Errorf("request = %s %s?%s, want GET /api/schema/?format=json", got.Method, got.URL.Path, got.URL.RawQuery)
	}
	if got.Header.Get("Authorization") != "Bearer "+testToken {
		t.Errorf("Schema didn't send the superuser's token")
	}
}

func TestSchemaFails(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status int
		size   int
		url    string
		want   string
	}{
		{name: "an error", status: http.StatusForbidden, want: "NetBox answered 403, not 200"},
		{name: "too large", status: http.StatusOK, size: maxSchema + 1, want: "over 67108864 bytes"},
		{name: "another machine", url: "http://192.0.2.10:8000", want: "isn't on this machine"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(strings.Repeat("x", tt.size)))
			}))
			t.Cleanup(srv.Close)
			nb := &NetBox{URL: srv.URL, Token: config.NewSecret(testToken)}
			if tt.url != "" {
				nb.URL = tt.url
			}
			if _, err := nb.Schema(t.Context()); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Schema error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}
