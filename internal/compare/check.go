package compare

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
)

// Compare's matching relies on what these check: a value it can't read as
// its field's would drop out of a rule or a row, and change an outcome
// without a word. model's Spec.DecodeAPI makes NetBox's objects so, and a
// format's definitions are validated against their specs, so a failure
// here means a bug, or an answer that didn't come through them.

// checkObserved checks that a NetBox object holds every field its spec
// describes, and nothing else, each of the field's kind or null where the
// field takes it, and every reference by a NetBox ID.
func checkObserved(s *model.Spec, fields map[string]model.Value) error {
	if err := described(s, fields); err != nil {
		return err
	}
	for i := range s.Fields {
		f := &s.Fields[i]
		v := fields[f.Name]
		if v == nil {
			return fmt.Errorf("has no %s", f.Name)
		}
		if err := checkKind(f, v); err != nil {
			return fmt.Errorf("%s %w", f.Name, err)
		}
		if ref, ok := v.(model.Reference); ok && ref.ID <= 0 {
			return fmt.Errorf("%s refers to no NetBox object", f.Name)
		}
	}
	return nil
}

// checkDefinition checks that a definition sets only fields its spec
// describes, each of the field's kind or null where the field takes it, with
// every reference to an object that stands on its own given by natural key
// alone, and children as checkChildren checks them, and that it sets its
// identity's fields, as its Identity gives them.
func (ix *index) checkDefinition(s *model.Spec, d *Definition) error {
	for _, name := range slices.Sorted(maps.Keys(d.Object.Fields)) {
		v := d.Object.Fields[name]
		if col := s.Collection(name); col != nil {
			if err := ix.checkChildren(s, col, v, d.Object.Fields); err != nil {
				return err
			}
			continue
		}
		f := s.Field(name)
		switch {
		case f == nil:
			return fmt.Errorf("sets %s, which the spec of %s doesn't describe", strconv.Quote(name), s.Type)
		case v == nil:
			return fmt.Errorf("sets %s to no value", name)
		}
		if err := checkKind(f, v); err != nil {
			return fmt.Errorf("%s %w", name, err)
		}
		if ref, ok := v.(model.Reference); ok && f.Ref.Within == "" && (ref.ID != 0 || len(ref.Key) == 0) {
			return fmt.Errorf("%s refers to a %s other than by its natural key", name, ref.Type)
		}
	}
	if len(d.Identity) != len(s.Identity) {
		return fmt.Errorf("has an identity of %d fields, but a %s's has %d", len(d.Identity), s.Type, len(s.Identity))
	}
	for _, name := range s.Identity {
		v := d.Object.Fields[name]
		switch {
		case v == nil || v.Kind() == model.KindNull:
			return fmt.Errorf("doesn't set %s, which is part of its identity", name)
		case !model.Equal(v, d.Identity[name]):
			return fmt.Errorf("sets %s to %s, but its identity says %s", name, v, describe(d.Identity[name]))
		}
	}
	return nil
}

// checkChildren checks a definition's children in collection col of its
// type s: each of the collection's type, setting only fields its spec
// describes, but not the one that refers to its parent, each of the field's
// kind or null where it takes it, with its key set and different from every
// other child's as NetBox would store it, and every reference to another
// child of the parent given by key alone and naming one the definition has.
// TrueUp doesn't compare nested collections, or a child's references to
// objects that stand on their own, yet.
func (ix *index) checkChildren(s *model.Spec, col *model.Collection, v model.Value, parent map[string]model.Value) error {
	children, ok := v.(model.Children)
	cs := ix.specs[col.Type]
	switch {
	case !ok:
		return fmt.Errorf("sets %s to %s, not children", col.Name, describe(v))
	case cs == nil:
		return fmt.Errorf("has %s, whose type %s has no spec", col.Name, col.Type)
	case col.Nested:
		return fmt.Errorf("has %s, a nested collection, which TrueUp doesn't compare yet", col.Name)
	}
	keys := map[string]int{}
	for i, c := range children {
		where := col.Name + "[" + strconv.Itoa(i) + "]"
		if c.Type != col.Type {
			return fmt.Errorf("%s is a %s, not a %s", where, strconv.Quote(c.Type), col.Type)
		}
		for _, name := range slices.Sorted(maps.Keys(c.Fields)) {
			f, v := cs.Field(name), c.Fields[name]
			switch {
			case f == nil:
				return fmt.Errorf("%s sets %s, which the spec of %s doesn't describe", where, strconv.Quote(name), cs.Type)
			case name == col.Parent:
				return fmt.Errorf("%s sets %s, which refers to its parent", where, name)
			case v == nil:
				return fmt.Errorf("%s sets %s to no value", where, name)
			}
			if err := checkKind(f, v); err != nil {
				return fmt.Errorf("%s %s %w", where, name, err)
			}
			ref, isRef := v.(model.Reference)
			switch {
			case !isRef:
			case f.Ref.Within != s.Type:
				return fmt.Errorf("%s %s refers to a %s outside the %s, which TrueUp doesn't compare in children yet", where, name, ref.Type, s.Type)
			case ref.ID != 0 || len(ref.Key) == 0:
				return fmt.Errorf("%s %s refers to a %s other than by its key", where, name, ref.Type)
			}
		}
		key, ok := ruleKey(model.Unique{Fields: col.Key}, normalized(cs, c.Fields, col.Key))
		if !ok {
			return fmt.Errorf("%s hasn't got its key, %s", where, strings.Join(col.Key, " and "))
		}
		if j, seen := keys[key]; seen {
			return fmt.Errorf("%s has the key of %s[%d], which NetBox keeps unique among a %s's %s", where, col.Name, j, s.Type, col.Name)
		}
		keys[key] = i
	}
	for i, c := range children {
		for _, name := range slices.Sorted(maps.Keys(c.Fields)) {
			if ref, isRef := c.Fields[name].(model.Reference); isRef && !holds(ix.specs, s, parent, ref) {
				return fmt.Errorf("%s[%d] %s refers to %s, which the definition hasn't got", col.Name, i, name, ref)
			}
		}
	}
	return nil
}

// normalized returns the named fields of a child, normalized as its spec
// says, as NetBox would store them.
func normalized(s *model.Spec, fields map[string]model.Value, names []string) map[string]model.Value {
	out := make(map[string]model.Value, len(names))
	for _, name := range names {
		if f, v := s.Field(name), fields[name]; f != nil && v != nil {
			out[name] = f.Normalize(v)
		}
	}
	return out
}

// holds reports whether an object of type s with these fields has a child
// that ref names by its collection's key, as NetBox would store it.
func holds(specs map[string]*model.Spec, s *model.Spec, fields map[string]model.Value, ref model.Reference) bool {
	cs := specs[ref.Type]
	for _, col := range s.Children {
		if col.Type != ref.Type || cs == nil {
			continue
		}
		want, ok := ruleKey(model.Unique{Fields: col.Key}, normalized(cs, ref.Key, col.Key))
		if !ok || len(ref.Key) != len(col.Key) {
			continue
		}
		children, _ := fields[col.Name].(model.Children)
		for _, c := range children {
			if key, ok := ruleKey(model.Unique{Fields: col.Key}, normalized(cs, c.Fields, col.Key)); ok && key == want {
				return true
			}
		}
	}
	return false
}

// checkKind checks that v is of field f's kind, or null where f is nullable,
// and that a reference refers to a type f can refer to.
func checkKind(f *model.Field, v model.Value) error {
	if v.Kind() == model.KindNull {
		if !f.Nullable {
			return errors.New("is null, which it can't be")
		}
		return nil
	}
	if v.Kind() != f.Kind {
		return fmt.Errorf("is a %s, not a %s", v.Kind(), f.Kind)
	}
	switch v := v.(type) {
	case model.Reference:
		if f.Ref == nil || !slices.Contains(f.Ref.Types, v.Type) {
			return fmt.Errorf("refers to a %s, which it can't", strconv.Quote(v.Type))
		}
	case model.Set:
		if f.Elem == nil {
			return errors.New("is a set of nothing the spec describes")
		}
		for _, e := range v {
			if e == nil {
				return errors.New("holds an element with no value")
			}
			if err := checkKind(f.Elem, e); err != nil {
				return fmt.Errorf("holds an element that %w", err)
			}
		}
	}
	return nil
}

func describe(v model.Value) string {
	if v == nil {
		return "nothing"
	}
	return v.String()
}
