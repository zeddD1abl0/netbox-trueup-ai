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
