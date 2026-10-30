//go:build integration

package dcim

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model/spectest"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/netboxtest"
)

var nb *netboxtest.NetBox

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
	code := m.Run()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer stopCancel()
	if err := nb.Stop(stopCtx); err != nil {
		fmt.Fprintln(os.Stderr, "stop NetBox:", err)
		return 1
	}
	return code
}

// TestLiveSchema checks the specs against the schema of the disposable
// NetBox, and that the snapshot in testdata is that schema, trimmed. `make
// schema` runs it with -update to rewrite the snapshot.
func TestLiveSchema(t *testing.T) {
	schema, err := nb.Schema(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	spectest.Live(t, schema, withPrototypes(), snapshot)
}

// created counts the objects the tests create, to name them apart.
var created atomic.Int64

func unique(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, created.Add(1))
}

// create makes an object through NetBox's REST API and returns the fields of
// NetBox's answer as JSON, or the error, which holds NetBox's reasons.
func create(t *testing.T, path string, body map[string]any) (map[string]json.RawMessage, error) {
	t.Helper()
	var out map[string]json.RawMessage
	err := nb.Post(t.Context(), path, body, &out)
	return out, err
}

func newManufacturer(t *testing.T) int64 {
	t.Helper()
	name := unique("maker")
	out, err := create(t, "/api/dcim/manufacturers/", map[string]any{"name": name, "slug": name})
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := json.Unmarshal(out["id"], &id); err != nil {
		t.Fatal(err)
	}
	return id
}

// observe reads a field as NetBox's API gives it, as a value of the field's
// kind: null, or a boolean, number, string or choice. It doesn't read
// references, which the defaults here don't include.
func observe(t *testing.T, f *model.Field, raw json.RawMessage) model.Value {
	t.Helper()
	if bytes.Equal(raw, []byte("null")) {
		return model.Null{}
	}
	var v model.Value
	var err error
	switch f.Kind {
	case model.KindBoolean:
		var b bool
		err = json.Unmarshal(raw, &b)
		v = model.Boolean(b)
	case model.KindDecimal:
		var n json.Number
		if err = json.Unmarshal(raw, &n); err == nil {
			v, err = model.ParseDecimal(n.String())
		}
	case model.KindString:
		var s string
		err = json.Unmarshal(raw, &s)
		v = model.String(s)
	case model.KindChoice:
		var c struct{ Value string }
		err = json.Unmarshal(raw, &c)
		v = model.Choice(c.Value)
	default:
		t.Fatalf("%s: can't read a %s", f.Name, f.Kind)
	}
	if err != nil {
		t.Fatalf("%s: read %s: %v", f.Name, raw, err)
	}
	return v
}

// factories create an object of each type with only its required fields,
// for TestDefaults.
var factories = map[string]func(t *testing.T) map[string]any{
	"dcim.manufacturer": func(*testing.T) map[string]any {
		name := unique("defaults")
		return map[string]any{"name": name, "slug": name}
	},
	"dcim.devicetype": func(t *testing.T) map[string]any {
		name := unique("defaults")
		return map[string]any{"manufacturer": newManufacturer(t), "model": name, "slug": name}
	},
}

// TestDefaults creates an object of each type the package has a spec of,
// with only its required fields, and checks that NetBox gives every other
// field the default its spec says, which NetBox's schema shows for few
// fields. It compares what NetBox gives without normalizing it, so a blank
// choice where the spec says null is a difference.
func TestDefaults(t *testing.T) {
	t.Parallel()
	for _, spec := range Specs() {
		factory, ok := factories[spec.Type]
		if !ok {
			t.Errorf("%s: TestDefaults has no way to create one; add it to factories", spec.Type)
			continue
		}
		obj, err := create(t, spec.Endpoint, factory(t))
		if err != nil {
			t.Fatal(err)
		}
		for i := range spec.Fields {
			f := &spec.Fields[i]
			if f.Required || f.ReadOnly {
				continue
			}
			raw, ok := obj[f.Name]
			if !ok {
				t.Errorf("%s.%s: NetBox's answer hasn't got it", spec.Type, f.Name)
				continue
			}
			if got := observe(t, f, raw); !model.Equal(got, f.Default) {
				t.Errorf("%s.%s: NetBox's default is %s, the spec's %s", spec.Type, f.Name, got, f.Default)
			}
		}
	}
}

// TestNormalizationAgainstNetBox writes values that the specs say NetBox
// changes, and checks that NetBox reads them back as Normalize does.
func TestNormalizationAgainstNetBox(t *testing.T) {
	t.Parallel()
	maker := newManufacturer(t)
	name := unique("normal")
	twoAndAHalf, err := model.ParseDecimal("2.50")
	if err != nil {
		t.Fatal(err)
	}
	written := map[string]model.Value{
		// Python's str.strip removes U+001C to U+001F, which Go's
		// unicode.IsSpace doesn't count as white space.
		"model":          model.String("\x1c\u00a0 " + name + " \t\x1f"),
		"description":    model.String("  padded \u3000"),
		"part_number":    model.String("\u2029"),
		"airflow":        model.Choice(""),
		"weight_unit":    model.Choice(""),
		"subdevice_role": model.Choice(""),
		"cooling_method": model.Choice(""),
		"u_height":       twoAndAHalf,
	}
	body := map[string]any{"manufacturer": maker, "slug": name}
	for field, v := range written {
		switch v := v.(type) {
		case model.String:
			body[field] = string(v)
		case model.Choice:
			body[field] = string(v)
		case model.Decimal:
			body[field] = json.Number(v.String())
		}
	}
	out, err := create(t, "/api/dcim/device-types/", body)
	if err != nil {
		t.Fatal(err)
	}
	for field, v := range written {
		f := deviceType.Field(field)
		got := observe(t, f, out[field])
		if want := f.Normalize(v); !model.Equal(want, got) {
			t.Errorf("%s: wrote %s, NetBox gives %s, Normalize %s", field, v, got, want)
		}
	}
}

// TestValidationAgainstNetBox writes values to NetBox and checks that it
// accepts those Validate accepts and refuses the others.
func TestValidationAgainstNetBox(t *testing.T) {
	t.Parallel()
	maker := newManufacturer(t)
	tests := []struct {
		field string
		json  any
		value model.Value
	}{
		{"u_height", 0.5, model.NewDecimal(5, 1)},
		{"u_height", 0, model.NewDecimal(0, 0)},
		{"u_height", 999.5, model.NewDecimal(9995, 1)},
		{"u_height", 1.25, model.NewDecimal(125, 2)},
		{"u_height", 1.1, model.NewDecimal(11, 1)},
		{"u_height", -1, model.NewDecimal(-1, 0)},
		{"u_height", 1000, model.NewDecimal(1000, 0)},
		{"weight", 999999.99, model.NewDecimal(99999999, 2)},
		{"weight", 1000000, model.NewDecimal(1000000, 0)},
		{"weight", 12.345, model.NewDecimal(12345, 3)},
		{"weight", 0, model.NewDecimal(0, 0)},
		{"weight", -5, model.NewDecimal(-5, 0)},
		{"part_number", strings.Repeat("p", 50), model.String(strings.Repeat("p", 50))},
		{"part_number", strings.Repeat("p", 51), model.String(strings.Repeat("p", 51))},
		{"part_number", strings.Repeat("ä", 50), model.String(strings.Repeat("ä", 50))},
		{"airflow", "passive", model.Choice("passive")},
		{"airflow", "sideways", model.Choice("sideways")},
		{"cooling_method", "immersion", model.Choice("immersion")},
		{"subdevice_role", "child", model.Choice("child")},
		{"weight_unit", "st", model.Choice("st")},
	}
	for _, tt := range tests {
		name := unique("valid")
		body := map[string]any{"manufacturer": maker, "model": name, "slug": name, tt.field: tt.json}
		if tt.field == "subdevice_role" {
			body["u_height"] = 0 // a child device type is 0U
		}
		if tt.field == "weight" {
			body["weight_unit"] = "kg" // a weight needs a unit
		}
		_, nbErr := create(t, "/api/dcim/device-types/", body)
		f := deviceType.Field(tt.field)
		err := f.Validate(f.Normalize(tt.value))
		if (nbErr == nil) != (err == nil) {
			t.Errorf("%s = %v: NetBox says %v, Validate %v", tt.field, tt.json, nbErr, err)
		}
	}
	for _, slug := range []string{"Upper_and-lower-1", "has space", "dotted.slug", "ünï"} {
		_, nbErr := create(t, "/api/dcim/device-types/", map[string]any{"manufacturer": maker, "model": unique("slug"), "slug": slug})
		err := deviceType.Field("slug").Validate(model.String(slug))
		if (nbErr == nil) != (err == nil) {
			t.Errorf("slug %q: NetBox says %v, Validate %v", slug, nbErr, err)
		}
	}
}
