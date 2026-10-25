package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/netbox"
)

// checkToken is a fake token that the fake NetBox below accepts.
const checkToken = "nbt_key.check" //nolint:gosec // G101: a fake token for a fake NetBox

// fakeNetBox answers as NetBox 4.7 does the requests `netbox check` makes,
// reporting version, and answering 403 for the endpoints in denied. As in
// NetBox, a token it refuses fails every request.
func fakeNetBox(t *testing.T, version, authorization string, denied ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("netbox check sent %s %s", r.Method, r.URL.Path)
		}
		apiVersion := version[:strings.LastIndex(version, ".")]
		answer := func(status int, v any) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("API-Version", apiVersion)
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(v)
		}
		switch {
		case r.Header.Get("Authorization") != authorization:
			answer(http.StatusForbidden, map[string]any{"detail": "Invalid v2 token"})
		case r.URL.Path == "/api/status/":
			answer(http.StatusOK, map[string]any{"netbox-version": version})
		case r.URL.Path == "/api/authentication-check/":
			answer(http.StatusOK, map[string]any{"username": "trueup"})
		case slices.Contains(denied, r.URL.Path):
			answer(http.StatusForbidden, map[string]any{"detail": "You do not have permission to perform this action."})
		default:
			answer(http.StatusOK, map[string]any{"count": nil, "next": nil, "previous": nil, "results": []any{}})
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// checkEnv points TrueUp at srv with token, given through a token file, and
// without retries, so that failures are quick.
func checkEnv(t *testing.T, srv *httptest.Server, token string) []string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, dir, "token", token+"\n")
	return []string{
		"TRUEUP_NETBOX_URL=" + srv.URL,
		"TRUEUP_NETBOX_TOKEN_FILE=" + filepath.Join(dir, "token"),
		"TRUEUP_NETBOX_RETRIES=0",
	}
}

func TestNetBoxCheckText(t *testing.T) {
	t.Parallel()
	srv := fakeNetBox(t, "4.7.2", "Bearer "+checkToken)
	r := runCLI(t, nil, checkEnv(t, srv, checkToken), "netbox", "check")
	if r.code != exitOK || r.stderr != "" {
		t.Fatalf("exit code = %d\nstderr: %s", r.code, r.stderr)
	}
	want := `NetBox:  ` + srv.URL + `, release 4.7.2 (API 4.7)
Token:   v2, for user trueup

OBJECT TYPE                     ENDPOINT                                  READABLE
dcim.manufacturer               /api/dcim/manufacturers/                  yes
dcim.devicetype                 /api/dcim/device-types/                   yes
dcim.consoleporttemplate        /api/dcim/console-port-templates/         yes
dcim.consoleserverporttemplate  /api/dcim/console-server-port-templates/  yes
dcim.powerporttemplate          /api/dcim/power-port-templates/           yes
dcim.poweroutlettemplate        /api/dcim/power-outlet-templates/         yes
dcim.interfacetemplate          /api/dcim/interface-templates/            yes
dcim.frontporttemplate          /api/dcim/front-port-templates/           yes
dcim.rearporttemplate           /api/dcim/rear-port-templates/            yes
dcim.modulebaytemplate          /api/dcim/module-bay-templates/           yes
dcim.devicebaytemplate          /api/dcim/device-bay-templates/           yes
dcim.inventoryitemtemplate      /api/dcim/inventory-item-templates/       yes

The token can read every object type TrueUp reads. Constraints on its permissions
aren't checked: where they apply, TrueUp sees only the objects they allow.
`
	if diff := cmp.Diff(want, r.stdout); diff != "" {
		t.Errorf("netbox check (-want +got):\n%s", diff)
	}
}

func TestNetBoxCheckJSON(t *testing.T) {
	t.Parallel()
	denied := []string{"/api/dcim/device-types/", "/api/dcim/inventory-item-templates/"}
	srv := fakeNetBox(t, "4.7.0", "Bearer "+checkToken, denied...)
	r := runCLI(t, nil, checkEnv(t, srv, checkToken), "netbox", "check", "-o", "json")
	if r.code != exitCannotRead {
		t.Errorf("exit code = %d, want %d\nstderr: %s", r.code, exitCannotRead, r.stderr)
	}
	wantErr := "Error: the token can't read 2 of the 12 object types TrueUp reads: grant its user the view permission on dcim.devicetype, dcim.inventoryitemtemplate\n"
	if r.stderr != wantErr {
		t.Errorf("stderr = %q, want %q", r.stderr, wantErr)
	}

	var got checkJSON
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("netbox check -o json printed %q: %v", r.stdout, err)
	}
	want := checkJSON{
		URL: srv.URL, NetBoxVersion: "4.7.0", APIVersion: "4.7", User: "trueup", TokenVersion: 2,
		Warnings: []warningJSON{},
	}
	for _, rt := range netbox.ReadTypes() {
		want.ObjectTypes = append(want.ObjectTypes, objectTypeJSON{
			ObjectType: rt.Name, Endpoint: rt.Endpoint, Readable: !slices.Contains(denied, rt.Endpoint),
		})
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("netbox check -o json (-want +got):\n%s", diff)
	}
	// The field names are public interface.
	for _, field := range []string{`"url"`, `"netbox_version"`, `"api_version"`, `"user"`, `"token_version"`, `"ok": false`, `"object_types"`, `"object_type"`, `"endpoint"`, `"readable"`, `"warnings": []`} {
		if !strings.Contains(r.stdout, field) {
			t.Errorf("netbox check -o json has no %s:\n%s", field, r.stdout)
		}
	}
}

func TestNetBoxCheckWarnsAboutV1Tokens(t *testing.T) {
	t.Parallel()
	const v1 = "v1tokenv1tokenv1tokenv1tokenv1tokenv1tok"
	srv := fakeNetBox(t, "4.7.2", "Token "+v1)
	r := runCLI(t, nil, checkEnv(t, srv, v1), "netbox", "check")
	if r.code != exitOK {
		t.Fatalf("exit code = %d\nstderr: %s", r.code, r.stderr)
	}
	if !strings.Contains(r.stdout, "Token:   v1, for user trueup") || !strings.Contains(r.stdout, "Warnings:\n  netbox.token is a v1 token, which NetBox deprecated in 4.6") {
		t.Errorf("stdout doesn't show the v1 token and its warning:\n%s", r.stdout)
	}
	if !strings.Contains(r.stderr, "level=WARN msg=\"netbox.token is a v1 token") {
		t.Errorf("the warning wasn't logged: %q", r.stderr)
	}
	if strings.Contains(r.stdout+r.stderr, v1) {
		t.Error("the output holds the token")
	}
}

func TestNetBoxCheckFails(t *testing.T) {
	t.Parallel()

	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	tests := []struct {
		name       string
		env        func(t *testing.T) []string
		wantCode   int
		wantStderr string // a substring
	}{
		{
			name: "an unsupported release",
			env: func(t *testing.T) []string {
				return checkEnv(t, fakeNetBox(t, "4.6.3", "Bearer "+checkToken), checkToken)
			},
			wantCode:   exitFailure,
			wantStderr: "NetBox 4.6.3 isn't supported: TrueUp works with NetBox 4.7 only\n",
		},
		{
			name: "a refused token",
			env: func(t *testing.T) []string {
				return checkEnv(t, fakeNetBox(t, "4.7.2", "Bearer nbt_other.token"), checkToken)
			},
			wantCode:   exitFailure,
			wantStderr: `NetBox refused the token: GET /api/status/: NetBox answered 403 Forbidden: "Invalid v2 token"`,
		},
		{
			name:       "NetBox can't be reached",
			env:        func(t *testing.T) []string { return checkEnv(t, closed, checkToken) },
			wantCode:   exitFailure,
			wantStderr: "Error: connect to NetBox at " + closed.URL + ": read NetBox's status: GET /api/status/: dial tcp",
		},
		{
			name:     "no settings",
			env:      func(*testing.T) []string { return nil },
			wantCode: exitConfig,
			wantStderr: "Error: netbox.url isn't set: set --netbox-url, TRUEUP_NETBOX_URL or netbox.url in the config file\n" +
				"netbox.token isn't set: set --netbox-token-file, TRUEUP_NETBOX_TOKEN_FILE, or netbox.token_file in the config file\n",
		},
		{
			name:       "an invalid setting",
			env:        func(*testing.T) []string { return []string{"TRUEUP_NETBOX_TIMEOUT=forever"} },
			wantCode:   exitConfig,
			wantStderr: `netbox.timeout (env TRUEUP_NETBOX_TIMEOUT): "forever" is not a duration`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := runCLI(t, nil, tt.env(t), "netbox", "check", "-o", "json")
			if r.code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", r.code, tt.wantCode)
			}
			if r.stdout != "" {
				t.Errorf("stdout = %q, want it empty", r.stdout)
			}
			if !strings.Contains(r.stderr, tt.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", r.stderr, tt.wantStderr)
			}
		})
	}
}

// Text from NetBox in an error can't drive the terminal.
func TestErrorsEscapeControlCharacters(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"plain":                      "plain",
		"two\n  lines":               "two\n  lines",
		"a \x1b[2Jclear":             `a \x1b[2Jclear`,
		"a\rreturn and a \u0085 too": `a\rreturn and a \u0085 too`,
		"user\u202egnp.exe":          `user\u202egnp.exe`,
		"zero\u200bwidth":            `zero\u200bwidth`,
		"8-bit \x9b2J csi":           `8-bit \x9b2J csi`,
		"é then \xff":                `é then \xff`,
	}
	for in, want := range tests {
		if got := escapeControls(in); got != want {
			t.Errorf("escapeControls(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCellQuotes(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"trueup": "trueup", "a\tb": `"a\tb"`, "user\u202egnp": `"user\u202egnp"`, "csi\x9b2J": `"csi\x9b2J"`,
	} {
		if got := cell(in); got != want {
			t.Errorf("cell(%q) = %q, want %q", in, got, want)
		}
	}
}
