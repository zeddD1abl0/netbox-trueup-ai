package compare

import (
	"testing"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
)

func TestRuleKeyTakesASetInAnyOrder(t *testing.T) {
	t.Parallel()
	u := model.Unique{Fields: []string{"name", "tags"}}
	a, okA := ruleKey(u, map[string]model.Value{"name": model.String("x"), "tags": model.Set{model.String("a"), model.String("b")}})
	b, okB := ruleKey(u, map[string]model.Value{"name": model.String("x"), "tags": model.Set{model.String("b"), model.String("a")}})
	c, _ := ruleKey(u, map[string]model.Value{"name": model.String("x"), "tags": model.Set{model.String("a")}})
	if !okA || !okB || a != b || a == c {
		t.Errorf("keys %q, %q and %q: want the first two equal, and the third different", a, b, c)
	}
}

func TestFindRefusesAKeyItCantMatch(t *testing.T) {
	t.Parallel()
	// An identity that holds a reference, which a key gives by natural key
	// in turn, can't be matched without resolving that first.
	maker := &model.Spec{
		Type: "test.maker", Identity: []string{"name"}, Unique: []model.Unique{{Fields: []string{"name"}}},
		Fields: []model.Field{{Name: "name", Kind: model.KindString}},
	}
	thing := &model.Spec{
		Type: "test.thing", Identity: []string{"maker", "name"}, Unique: []model.Unique{{Fields: []string{"maker", "name"}}},
		Fields: []model.Field{
			{Name: "maker", Kind: model.KindReference, Ref: &model.Ref{Types: []string{"test.maker"}}},
			{Name: "name", Kind: model.KindString},
		},
	}
	ix, err := newIndex([]*model.Spec{maker, thing}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ix.find(model.Reference{Type: "test.thing", Key: map[string]model.Value{
		"maker": model.Reference{Type: "test.maker", Key: map[string]model.Value{"name": model.String("Acme")}},
		"name":  model.String("A1"),
	}})
	if err == nil {
		t.Error("find matched a key holding a reference that isn't resolved")
	}
}

// TestIndexFilesAChildUnderItsFlatHolder checks that a type one parent
// carries nested and another lists flat is filed under the flat one.
func TestIndexFilesAChildUnderItsFlatHolder(t *testing.T) {
	t.Parallel()
	thing := &model.Spec{
		Type: "test.thing", Identity: []string{"name"}, Unique: []model.Unique{{Fields: []string{"name"}}},
		Fields:   []model.Field{{Name: "name", Kind: model.KindString}},
		Children: []model.Collection{{Name: "parts", Type: "test.part", Parent: "thing", Key: []string{"name"}}},
	}
	other := &model.Spec{
		Type: "test.other", Identity: []string{"name"}, Unique: []model.Unique{{Fields: []string{"name"}}},
		Fields:   []model.Field{{Name: "name", Kind: model.KindString}},
		Children: []model.Collection{{Name: "parts", Type: "test.part", Key: []string{"name"}, Nested: true}},
	}
	part := &model.Spec{
		Type: "test.part", Unique: []model.Unique{{Fields: []string{"thing", "name"}}},
		Fields: []model.Field{
			{Name: "thing", Kind: model.KindReference, Nullable: true, Ref: &model.Ref{Types: []string{"test.thing"}}},
			{Name: "name", Kind: model.KindString},
		},
	}
	// The nesting holder first, so that it is met before the flat one.
	ix, err := newIndex([]*model.Spec{other, thing, part}, []Observed{
		{ID: 1, Object: model.Object{Type: "test.thing", Fields: map[string]model.Value{"name": model.String("a")}}},
		{ID: 2, Object: model.Object{Type: "test.part", Fields: map[string]model.Value{
			"thing": model.Reference{Type: "test.thing", ID: 1}, "name": model.String("p"),
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := ix.children["test.thing"][1]["parts"]; len(got) != 1 || got[0] != 2 {
		t.Errorf("thing #1's parts are %v, want #2", got)
	}
}
