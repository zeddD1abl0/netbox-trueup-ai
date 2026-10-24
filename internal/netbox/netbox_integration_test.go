//go:build integration

package netbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/netboxtest"
)

// The NetBox that TestMain starts, and what it seeds it with through
// netboxtest, never through the client under test.
var (
	nb *netboxtest.NetBox
	// deviceTypes are three device types' IDs, and seeded the IDs of their
	// interface templates, created interleaved so that their IDs mix.
	deviceTypes [3]int64
	seeded      = map[int64][]int64{}
	// readerToken is a read-only v2 token for a user that may view
	// manufacturers only, and readerV1Token a v1 token for the same user.
	readerToken, readerV1Token config.Secret
)

const (
	readerName     = "trueup-reader"
	readerPassword = "Insecure-test-only-reader-1" // a dummy, for a disposable NetBox; NetBox wants a digit and both cases
)

// templateCounts is how many interface templates each device type has.
var templateCounts = [3]int{12, 7, 5}

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	ctx, cancel := context.WithTimeoutCause(context.Background(), 25*time.Minute,
		errors.New("NetBox didn't start within 25 minutes"))
	defer cancel()
	var err error
	if nb, err = netboxtest.Start(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "start NetBox:", err)
		if cause := context.Cause(ctx); cause != nil {
			fmt.Fprintln(os.Stderr, "Cause:", cause)
		}
		return 1
	}
	code := 1
	if err := seed(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "seed NetBox:", err)
	} else {
		code = m.Run()
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer stopCancel()
	if err := nb.Stop(stopCtx); err != nil {
		fmt.Fprintln(os.Stderr, "stop NetBox:", err)
		code = 1
	}
	return code
}

// seed creates a manufacturer, three device types with interface templates,
// and a user who may only view manufacturers, with tokens.
func seed(ctx context.Context) error {
	type created struct {
		ID    int64  `json:"id"`
		Token string `json:"token"`
	}
	var mfr created
	if err := nb.Post(ctx, "/api/dcim/manufacturers/", map[string]any{"name": "TrueUp Test", "slug": "trueup-test"}, &mfr); err != nil {
		return err
	}
	var dts []created
	var body []map[string]any
	for i := range deviceTypes {
		model := fmt.Sprintf("Model %c", 'A'+i)
		body = append(body, map[string]any{"manufacturer": mfr.ID, "model": model, "slug": strings.ToLower(strings.ReplaceAll(model, " ", "-"))})
	}
	if err := nb.Post(ctx, "/api/dcim/device-types/", body, &dts); err != nil {
		return err
	}
	if len(dts) != len(deviceTypes) {
		return fmt.Errorf("NetBox made %d device types, not %d", len(dts), len(deviceTypes))
	}
	for i, dt := range dts {
		deviceTypes[i] = dt.ID
	}

	body = nil
	var owners []int64
	for n := range slices.Max(templateCounts[:]) {
		for i, count := range templateCounts {
			if n < count {
				body = append(body, map[string]any{"device_type": deviceTypes[i], "name": fmt.Sprintf("eth%d", n), "type": "1000base-t"})
				owners = append(owners, deviceTypes[i])
			}
		}
	}
	var made []created
	if err := nb.Post(ctx, "/api/dcim/interface-templates/", body, &made); err != nil {
		return err
	}
	if len(made) != len(body) {
		return fmt.Errorf("NetBox made %d interface templates, not %d", len(made), len(body))
	}
	for i, t := range made {
		seeded[owners[i]] = append(seeded[owners[i]], t.ID)
	}

	var user created
	if err := nb.Post(ctx, "/api/users/users/", map[string]any{"username": readerName, "password": readerPassword}, &user); err != nil {
		return err
	}
	perm := map[string]any{"name": "view manufacturers", "object_types": []string{"dcim.manufacturer"}, "actions": []string{"view"}, "users": []int64{user.ID}}
	if err := nb.Post(ctx, "/api/users/permissions/", perm, nil); err != nil {
		return err
	}
	var err error
	if readerToken, err = nb.UserToken(ctx, readerName, readerPassword); err != nil {
		return err
	}
	var v1 created
	if err := nb.Post(ctx, "/api/users/tokens/", map[string]any{"user": user.ID, "version": 1, "write_enabled": false, "description": "v1 token"}, &v1); err != nil {
		return err
	}
	if v1.Token == "" || strings.HasPrefix(v1.Token, "nbt_") {
		return errors.New("NetBox didn't make a v1 token")
	}
	readerV1Token = config.NewSecret(v1.Token)
	return nil
}

// connect connects to the test NetBox with token, logging at debug level to
// logs if it isn't nil.
func connect(t *testing.T, token config.Secret, logs *bytes.Buffer) *Client {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	if logs != nil {
		logger = slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	c, err := Connect(t.Context(), Options{URL: nb.URL, Token: token, Logger: logger})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return c
}

func TestConnectToNetBox(t *testing.T) {
	t.Parallel()
	admin, _, err := netboxtest.Superuser()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		token config.Secret
		want  Info
	}{
		{name: "the superuser's v2 token", token: nb.Token, want: Info{User: admin, TokenVersion: 2}},
		{name: "a read-only v2 token", token: readerToken, want: Info{User: readerName, TokenVersion: 2}},
		{name: "a v1 token", token: readerV1Token, want: Info{User: readerName, TokenVersion: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			info := connect(t, tt.token, &logs).Info()
			if info.URL != nb.URL || info.Version != netboxtest.Version || info.APIVersion != SupportedVersion {
				t.Errorf("Info = %+v, want NetBox %s at %s", info, netboxtest.Version, nb.URL)
			}
			if info.User != tt.want.User || info.TokenVersion != tt.want.TokenVersion {
				t.Errorf("Info = %+v, want user %s and token version %d", info, tt.want.User, tt.want.TokenVersion)
			}
			if wantWarning := tt.want.TokenVersion == 1; (len(info.Warnings) == 1) != wantWarning {
				t.Errorf("warnings = %q", info.Warnings)
			}
			// The token never reaches the log, though requests do.
			secret := tt.token.Reveal()
			if _, s, ok := strings.Cut(secret, "."); ok {
				secret = s
			}
			if strings.Contains(logs.String(), secret) {
				t.Errorf("the log holds the token")
			}
			if !strings.Contains(logs.String(), `"msg":"NetBox request"`) {
				t.Errorf("requests weren't logged:\n%s", logs.String())
			}
		})
	}
}

func TestConnectRefusesAWrongToken(t *testing.T) {
	t.Parallel()
	key, _, _ := strings.Cut(strings.TrimPrefix(nb.Token.Reveal(), "nbt_"), ".")
	wrong := config.NewSecret("nbt_" + key + "." + strings.Repeat("0", 40))
	_, err := Connect(t.Context(), Options{URL: nb.URL, Token: wrong})
	if !errors.Is(err, ErrTokenRejected) || !strings.Contains(err.Error(), `"Invalid v2 token"`) {
		t.Errorf("Connect error = %v, want ErrTokenRejected for an invalid v2 token", err)
	}
}

// List pages through NetBox's cursor pagination, whether or not the last
// page is full.
func TestListPagesThroughNetBox(t *testing.T) {
	t.Parallel()
	var want []int64
	for _, ids := range seeded {
		want = append(want, ids...)
	}
	slices.Sort(want)

	for _, size := range []int{5, 8, 24, 1000} {
		t.Run(fmt.Sprintf("pages of %d", size), func(t *testing.T) {
			t.Parallel()
			c := connect(t, nb.Token, nil)
			c.pageSize = size
			var got []int64
			err := c.List(t.Context(), "/api/dcim/interface-templates/", nil, func(raw json.RawMessage) error {
				id, err := objectID(raw)
				got = append(got, id)
				return err
			})
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if !slices.Equal(want, got) {
				t.Errorf("listed %v, want %v", got, want)
			}
		})
	}
}

// ListByParent gets the children of several parents, in batches, and NetBox
// filters them as the client expects.
func TestListByParentInNetBox(t *testing.T) {
	t.Parallel()
	c := connect(t, nb.Token, nil)
	c.pageSize = 4
	c.maxFilterBytes = len("device_type_id=1&") // one parent per request

	parents := []int64{deviceTypes[0], deviceTypes[2]}
	got := map[int64][]int64{}
	err := c.ListByParent(t.Context(), "/api/dcim/interface-templates/", "device_type", parents, nil, func(raw json.RawMessage) error {
		parent, err := parentID(raw, "device_type")
		if err != nil {
			return err
		}
		id, err := objectID(raw)
		got[parent] = append(got[parent], id)
		return err
	})
	if err != nil {
		t.Fatalf("ListByParent: %v", err)
	}
	for _, p := range parents {
		if !slices.Equal(seeded[p], got[p]) {
			t.Errorf("device type %d has templates %v, want %v", p, got[p], seeded[p])
		}
	}
	if len(got) != 2 {
		t.Errorf("listed templates of %d device types, want 2", len(got))
	}
}

// NetBox refuses the whole request if a parent is missing.
func TestListByParentWithAMissingParent(t *testing.T) {
	t.Parallel()
	c := connect(t, nb.Token, nil)
	err := c.ListByParent(t.Context(), "/api/dcim/interface-templates/", "device_type", []int64{deviceTypes[0], 999999}, nil, func(json.RawMessage) error { return nil })
	if !isStatus(err, http.StatusBadRequest) || !strings.Contains(err.Error(), "device_type_id") {
		t.Errorf("ListByParent error = %v, want 400 about device_type_id", err)
	}
}

// A token whose user lacks a view permission gets 403 for that type.
func TestCheckReadInNetBox(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		token    config.Secret
		readable []string
	}{
		{name: "the superuser", token: nb.Token, readable: names(ReadTypes())},
		{name: "a user who may view manufacturers", token: readerToken, readable: []string{"dcim.manufacturer"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			checks, err := connect(t, tt.token, nil).CheckRead(t.Context(), ReadTypes())
			if err != nil {
				t.Fatalf("CheckRead: %v", err)
			}
			var readable []string
			for _, c := range checks {
				if c.Readable {
					readable = append(readable, c.Type.Name)
				}
			}
			if !slices.Equal(tt.readable, readable) {
				t.Errorf("readable types = %v, want %v", readable, tt.readable)
			}
		})
	}
}

func names(types []ObjectType) []string {
	var out []string
	for _, t := range types {
		out = append(out, t.Name)
	}
	return out
}
