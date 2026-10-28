package dcim

import (
	"slices"
	"testing"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model/spectest"
)

// snapshot is the schema the specs are checked against, which the
// integration test keeps in step with the disposable NetBox's.
const snapshot = "testdata/openapi.json"

func TestSpecs(t *testing.T) {
	t.Parallel()
	if err := model.Check(Specs()); err != nil {
		t.Errorf("the package's specs aren't consistent on their own:\n%v", err)
	}
	spectest.Run(t, withPrototypes(), snapshot)
}

func TestSpecsInDependencyOrder(t *testing.T) {
	t.Parallel()
	var seen []string
	for _, s := range Specs() {
		for _, d := range s.DependsOn {
			if !slices.Contains(seen, d) {
				t.Errorf("Specs lists %s before %s, which it depends on", s.Type, d)
			}
		}
		seen = append(seen, s.Type)
	}
}

// TestNormalization checks the examples in docs/architecture.md,
// "Normalization", with the device type's spec.
func TestNormalization(t *testing.T) {
	t.Parallel()
	same := func(field string, a, b model.Value) {
		t.Helper()
		f := deviceType.Field(field)
		if na, nb := f.Normalize(a), f.Normalize(b); !model.Equal(na, nb) {
			t.Errorf("%s: %s and %s normalize to %s and %s, which differ", field, a, b, na, nb)
		}
	}
	different := func(field string, a, b model.Value) {
		t.Helper()
		f := deviceType.Field(field)
		if model.Equal(f.Normalize(a), f.Normalize(b)) {
			t.Errorf("%s: %s and %s normalize to the same value", field, a, b)
		}
	}
	oneU, err := model.ParseDecimal("1.0")
	if err != nil {
		t.Fatal(err)
	}
	same("u_height", model.NewDecimal(1, 0), oneU)
	different("u_height", model.NewDecimal(1, 0), model.NewDecimal(15, 1))
	same("airflow", model.Choice(""), model.Null{})
	same("subdevice_role", model.Choice(""), model.Null{})
	different("airflow", model.Choice("passive"), model.Null{})
	same("model", model.String(" PowerEdge R670\n"), model.String("PowerEdge R670"))
	different("model", model.String("PowerEdge R670"), model.String("poweredge r670"))
	same("description", model.String("\t"), model.String(""))
}

// TestValidation checks a few of NetBox's rules through the specs, as a
// definition would meet them.
func TestValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		field string
		v     model.Value
		ok    bool
	}{
		{"u_height", model.NewDecimal(5, 1), true},
		{"u_height", model.NewDecimal(25, 2), false},
		{"u_height", model.NewDecimal(15, 1), true},
		{"u_height", model.NewDecimal(11, 1), false},
		{"u_height", model.NewDecimal(-1, 0), false},
		{"weight", model.NewDecimal(123456, 2), true},
		{"weight", model.Null{}, true},
		{"slug", model.String("poweredge-r670"), true},
		{"slug", model.String("PowerEdge R670"), false},
		{"model", model.String(""), false},
		{"subdevice_role", model.Choice("parent"), true},
		{"subdevice_role", model.Choice("sibling"), false},
		{"cooling_method", model.Choice("immersion"), true},
		{"weight_unit", model.Choice("kg"), true},
		{"airflow", model.Choice("front-to-rear"), true},
		{"end_of_life", model.Null{}, true},
		{"is_full_depth", model.Null{}, false},
		{"manufacturer", model.Reference{Type: "dcim.manufacturer", Key: map[string]model.Value{"name": model.String("Dell")}}, true},
	}
	for _, tt := range tests {
		err := deviceType.Field(tt.field).Validate(tt.v)
		if (err == nil) != tt.ok {
			t.Errorf("%s.Validate(%s) = %v, want ok %t", tt.field, tt.v, err, tt.ok)
		}
	}
}
