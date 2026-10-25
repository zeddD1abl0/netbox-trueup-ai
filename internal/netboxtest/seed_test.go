package netboxtest

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
)

const testToken = "nbt_" + testKey + "." + testSecret

func TestPost(t *testing.T) {
	t.Parallel()
	var authorization string
	srv, rec := fakeNetBox(t, http.StatusCreated, `{"id": 7, "name": "Acme"}`)
	srv.Config.Handler = recordAuthorization(srv.Config.Handler, &authorization)
	nb := &NetBox{URL: srv.URL, Token: config.NewSecret(testToken)}

	var out struct {
		ID int `json:"id"`
	}
	body := map[string]any{"name": "Acme", "slug": "acme"}
	if err := nb.Post(t.Context(), "/api/dcim/manufacturers/", body, &out); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if out.ID != 7 {
		t.Errorf("Post decoded %+v, want ID 7", out)
	}
	if diff := cmp.Diff(body, rec.body); diff != "" {
		t.Errorf("request body (-want +got):\n%s", diff)
	}
	if rec.method != http.MethodPost || rec.path != "/api/dcim/manufacturers/" || rec.contentType != "application/json" {
		t.Errorf("request = %s %s with Content-Type %q", rec.method, rec.path, rec.contentType)
	}
	if authorization != "Bearer "+testToken {
		t.Errorf("Post sent an Authorization header of %d bytes, not the superuser's token", len(authorization))
	}
}

func TestPostFails(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		url    string // "" for the fake's
		path   string
		status int
		body   string
		want   string
	}{
		{name: "an error", path: "/api/dcim/manufacturers/", status: http.StatusBadRequest, body: `{"slug": ["exists"]}`, want: `NetBox answered 400, not 201: "{\"slug\": [\"exists\"]}"`},
		{name: "not created", path: "/api/users/tokens/", status: http.StatusOK, body: `{"token": "` + testSecret + `"}`, want: "NetBox answered 200, not 201"},
		{name: "not an API path", path: "/dcim/manufacturers/", want: "the path must be an API path"},
		{name: "a query", path: "/api/dcim/manufacturers/?background=true", want: "the path must be an API path"},
		{name: "another machine", url: "http://192.0.2.10:8000", path: "/api/dcim/manufacturers/", want: "isn't on this machine"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv, _ := fakeNetBox(t, tt.status, tt.body)
			nb := &NetBox{URL: srv.URL, Token: config.NewSecret(testToken)}
			if tt.url != "" {
				nb.URL = tt.url
			}
			err := nb.Post(t.Context(), tt.path, map[string]any{"name": "Acme"}, nil)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Post error = %v, want it to contain %q", err, tt.want)
			}
			if strings.Contains(err.Error(), testSecret) {
				t.Errorf("error %q repeats a secret", err)
			}
		})
	}
}

func TestUserToken(t *testing.T) {
	t.Parallel()

	srv, rec := fakeNetBox(t, http.StatusCreated, tokenAnswer(t, `"write_enabled": false`))
	nb := &NetBox{URL: srv.URL}
	tok, err := nb.UserToken(t.Context(), "reader", testPassword)
	if err != nil {
		t.Fatalf("UserToken: %v", err)
	}
	if got, want := tok.Reveal(), "nbt_"+testKey+"."+testSecret; got != want {
		t.Errorf("token = %q, want %q", got, want)
	}
	wantBody := map[string]any{"username": "reader", "password": testPassword, "description": "netboxtest", "write_enabled": false}
	if diff := cmp.Diff(wantBody, rec.body); diff != "" {
		t.Errorf("request body (-want +got):\n%s", diff)
	}

	// A token that can write isn't what was asked for.
	srv, _ = fakeNetBox(t, http.StatusCreated, tokenAnswer(t, ""))
	nb = &NetBox{URL: srv.URL}
	if _, err := nb.UserToken(t.Context(), "reader", testPassword); err == nil || !strings.Contains(err.Error(), "NetBox made a token that can write, not a read-only one") {
		t.Errorf("UserToken error = %v, want one about a token that can write", err)
	}
}

// recordAuthorization records each request's Authorization header in got.
func recordAuthorization(next http.Handler, got *string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*got = r.Header.Get("Authorization")
		next.ServeHTTP(w, r)
	})
}
