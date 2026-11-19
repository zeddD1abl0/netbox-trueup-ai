package compare_test

import (
	"testing"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/compare"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/format"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model/dcim"
)

func TestUniqueValues(t *testing.T) {
	t.Parallel()
	withChildren := deviceType("Dell", "R670", "dell-r670", map[string]model.Value{
		"part_number": model.String("210-BKRB"),
		"airflow":     model.Choice("front-to-rear"),
		"interfaces":  model.Children{child("interface", "eth0", nil)},
	})
	withChildren.Metadata = map[string]model.Value{"front_image": model.Boolean(true)}
	for _, tt := range []struct {
		name string
		in   compare.Definition
		want format.Definition
	}{
		{
			"a device type keeps its identity and slug, but not its other fields, children, metadata or unmapped lists",
			withChildren,
			format.Definition{
				Object: model.Object{Type: "dcim.devicetype", Fields: map[string]model.Value{
					"manufacturer": manufacturerRef("Dell"), "model": model.String("R670"), "slug": model.String("dell-r670"),
				}},
				Identity: map[string]model.Value{"manufacturer": manufacturerRef("Dell"), "model": model.String("R670")},
				Path:     "device-types/Dell/dell-r670.yaml",
			},
		},
		{
			"a derived manufacturer keeps its derived slug, but not the files it is derived from",
			manufacturer("Dell", "dell"),
			format.Definition{
				Object:   model.Object{Type: "dcim.manufacturer", Fields: map[string]model.Value{"name": model.String("Dell"), "slug": model.String("dell")}},
				Identity: map[string]model.Value{"name": model.String("Dell")},
				Derived:  []string{"slug"},
			},
		},
		{
			"a manufacturer without a slug has none",
			manufacturer("中文", ""),
			format.Definition{
				Object:   model.Object{Type: "dcim.manufacturer", Fields: map[string]model.Value{"name": model.String("中文")}},
				Identity: map[string]model.Value{"name": model.String("中文")},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := compare.UniqueValues(dcim.Specs(), tt.in.Definition)
			if err != nil {
				t.Fatal(err)
			}
			if !format.SameDefinition(got, tt.want) || len(got.DerivedFrom) > 0 {
				t.Errorf("UniqueValues = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestUniqueValuesKeepsReferences checks that a reference to an object that
// stands on its own is kept outside the uniqueness rules too, since
// matching resolves it, and NetBox having several objects it could be makes
// a conflict. No spec of dcim's has one yet.
func TestUniqueValuesKeepsReferences(t *testing.T) {
	t.Parallel()
	specs := append(dcim.Specs(), &model.Spec{
		Type:     "test.rack",
		Identity: []string{"name"},
		Unique:   []model.Unique{{Fields: []string{"name"}}},
		Fields: []model.Field{
			{Name: "name", Kind: model.KindString},
			{Name: "maker", Kind: model.KindReference, Ref: &model.Ref{Types: []string{"dcim.manufacturer"}}},
			{Name: "comments", Kind: model.KindString},
		},
	})
	in := format.Definition{
		Object: model.Object{Type: "test.rack", Fields: map[string]model.Value{
			"name": model.String("R1"), "maker": manufacturerRef("Dell"), "comments": model.String("x"),
		}},
		Identity: map[string]model.Value{"name": model.String("R1")},
	}
	got, err := compare.UniqueValues(specs, in)
	if err != nil {
		t.Fatal(err)
	}
	want := format.Definition{
		Object:   model.Object{Type: "test.rack", Fields: map[string]model.Value{"name": model.String("R1"), "maker": manufacturerRef("Dell")}},
		Identity: map[string]model.Value{"name": model.String("R1")},
	}
	if !format.SameDefinition(got, want) {
		t.Errorf("UniqueValues = %+v, want %+v", got, want)
	}
}

func TestUniqueValuesOfATypeWithoutASpec(t *testing.T) {
	t.Parallel()
	d := format.Definition{Object: model.Object{Type: "dcim.platform", Fields: map[string]model.Value{}}}
	if _, err := compare.UniqueValues(dcim.Specs(), d); err == nil {
		t.Error("UniqueValues of a type without a spec succeeded")
	}
}
