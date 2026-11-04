package netboxtest

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeWebUI answers as NetBox's web UI does to signing in and to an import:
// the login page sets a CSRF cookie, which every form must repeat, signing
// in sets a session and a new CSRF cookie, as Django's login does, and the
// import page redirects to what it imported, or shows itself again with
// page as its body when importOK is false.
type fakeWebUI struct {
	srv      *httptest.Server
	importOK bool
	page     string

	mu       sync.Mutex
	imported []string // the data of each import accepted
}

func newFakeWebUI(t *testing.T) *fakeWebUI {
	t.Helper()
	f := &fakeWebUI{importOK: true}
	username, password, err := Superuser()
	if err != nil {
		t.Fatal(err)
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		csrf := func() string {
			c, err := r.Cookie("csrftoken")
			if err != nil {
				return ""
			}
			return c.Value
		}
		if r.Method == http.MethodPost && (csrf() == "" || r.FormValue("csrfmiddlewaretoken") != csrf()) {
			http.Error(w, "CSRF verification failed", http.StatusForbidden)
			return
		}
		switch {
		case r.URL.Path == "/login/" && r.Method == http.MethodGet:
			setCookie(w, "csrftoken", "before-login")
			_, _ = w.Write([]byte("<form>login</form>"))
		case r.URL.Path == "/login/":
			if r.FormValue("username") != username || r.FormValue("password") != password {
				_, _ = w.Write([]byte(`<div class="form-text text-danger">Please enter a correct username and password.</div>`))
				return
			}
			setCookie(w, "sessionid", "signed-in")
			setCookie(w, "csrftoken", "after-login")
			http.Redirect(w, r, "/", http.StatusFound)
		case r.URL.Path == "/dcim/device-types/import/" || r.URL.Path == "/dcim/manufacturers/import/":
			if c, err := r.Cookie("sessionid"); err != nil || c.Value != "signed-in" || csrf() != "after-login" || r.FormValue("format") != "yaml" {
				http.Redirect(w, r, "/login/", http.StatusFound)
				return
			}
			if !f.importOK {
				_, _ = w.Write([]byte(f.page))
				return
			}
			f.mu.Lock()
			f.imported = append(f.imported, r.FormValue("data"))
			f.mu.Unlock()
			http.Redirect(w, r, "/dcim/device-types/?modified_by_request=abc", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// setCookie sets a cookie as the disposable NetBox does, over plain HTTP on
// loopback.
func setCookie(w http.ResponseWriter, name, value string) {
	//nolint:gosec // G124: a fake of NetBox's web UI over plain HTTP, where a Secure cookie would never be sent back.
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
}

func TestImport(t *testing.T) {
	t.Parallel()
	f := newFakeWebUI(t)
	nb := &NetBox{URL: f.srv.URL}
	const data = "---\nmanufacturer: Acme\nmodel: A1\nslug: a1\nu_height: 1\n"
	if err := nb.Import(t.Context(), "/dcim/device-types/import/", data); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(f.imported) != 1 || f.imported[0] != data {
		t.Errorf("imported %q, want %q", f.imported, data)
	}
}

func TestImportFails(t *testing.T) {
	t.Parallel()
	refused := `<html><div class="form-text text-danger" id="id_data_errors" role="alert">Invalid YAML data.</div>
<div class="toast-header text-bg-success">x</div><div class="toast-body">Logged in as admin.</div>
<div class="toast-header text-bg-danger"><i></i>Error</div>
  <div class="toast-body">
    Record 1 manufacturer: Object not found: Nobody &amp; co
  </div></html>`
	tests := []struct {
		name string
		url  string // "" for the fake's
		path string
		page string // the page a refused import shows, or "" to accept it
		want string
	}{
		{
			name: "refused", path: "/dcim/device-types/import/", page: refused,
			want: "NetBox refused it: Invalid YAML data.; Record 1 manufacturer: Object not found: Nobody & co",
		},
		{name: "refused without a reason", path: "/dcim/device-types/import/", page: "<html></html>", want: "its page gives no reason"},
		{name: "not an import page", path: "/api/dcim/device-types/", want: "the path must be an import page"},
		{name: "a query", path: "/dcim/device-types/import/?background_job=true", want: "the path must be an import page"},
		{name: "another machine", url: "http://192.0.2.10:8000", path: "/dcim/device-types/import/", want: "isn't on this machine"},
		{name: "a page that isn't there", path: "/dcim/nothing/import/", want: "NetBox answered 404"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFakeWebUI(t)
			f.importOK, f.page = tt.page == "", tt.page
			nb := &NetBox{URL: f.srv.URL}
			if tt.url != "" {
				nb.URL = tt.url
			}
			err := nb.Import(t.Context(), tt.path, "manufacturer: Nobody\n")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Import = %v, want an error with %q", err, tt.want)
			}
			if len(f.imported) != 0 {
				t.Errorf("imported %q", f.imported)
			}
		})
	}
}

func TestImportNeedsTheSuperusersPassword(t *testing.T) {
	t.Parallel()
	f := newFakeWebUI(t)
	// A NetBox whose superuser has another password refuses to sign in.
	inner := f.srv.Config.Handler
	f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/login/" {
			_ = r.ParseForm()
			r.PostForm.Set("password", "another")
			r.Form.Set("password", "another")
		}
		inner.ServeHTTP(w, r)
	})
	err := (&NetBox{URL: f.srv.URL}).Import(t.Context(), "/dcim/device-types/import/", "x: 1\n")
	if err == nil || !strings.Contains(err.Error(), "sign in to NetBox as admin: NetBox answered 200, not 302") {
		t.Errorf("Import = %v, want a failed sign-in", err)
	}
	_, password, _ := Superuser()
	if strings.Contains(err.Error(), password) {
		t.Errorf("the error %q repeats the password", err)
	}
}
