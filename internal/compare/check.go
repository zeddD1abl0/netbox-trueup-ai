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
// describes, and every nested collection, and nothing else, each field of
// its kind or null where the field takes it, every reference by a NetBox ID,
// and each nested child checked the same way. specs are the specs of every
// type.
func checkObserved(specs map[string]*model.Spec, s *model.Spec, fields map[string]model.Value) error {
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		if col := s.Collection(name); s.Field(name) == nil && (col == nil || !col.Nested) {
			return fmt.Errorf("sets %s, which the spec of %s doesn't describe", strconv.Quote(name), s.Type)
		}
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
	for i := range s.Children {
		col := &s.Children[i]
		if !col.Nested {
			continue
		}
		v := fields[col.Name]
		children, ok := v.(model.Children)
		cs := specs[col.Type]
		switch {
		case v == nil:
			return fmt.Errorf("has no %s", col.Name)
		case !ok:
			return fmt.Errorf("sets %s to %s, not children", col.Name, describe(v))
		case cs == nil:
			return fmt.Errorf("has %s, whose type %s has no spec", col.Name, col.Type)
		}
		for j, c := range children {
			where := col.Name + "[" + strconv.Itoa(j) + "]"
			if c.Type != col.Type {
				return fmt.Errorf("%s is a %s, not a %s", where, strconv.Quote(c.Type), col.Type)
			}
			if err := checkObserved(specs, cs, c.Fields); err != nil {
				return fmt.Errorf("%s %w", where, err)
			}
		}
	}
	return nil
}

// checkDefinition checks that a definition sets only fields its spec
// describes, each of the field's kind or null where the field takes it, with
// every reference to an object that stands on its own given by natural key
// alone, and children as checkChildren checks them, nested ones included,
// each reference to another child naming one the definition has, and that
// it sets its identity's fields, as its Identity gives them.
func (ix *index) checkDefinition(s *model.Spec, d *Definition) error {
	// The keys of the definition's children, by collection, and their
	// references to each other, checked once every collection's keys are
	// known.
	keys := map[string]map[string]int{}
	var refs []childRef
	for _, name := range slices.Sorted(maps.Keys(d.Object.Fields)) {
		v := d.Object.Fields[name]
		if col := s.Collection(name); col != nil {
			got, err := ix.checkChildren(s, s, col, v, "", &refs)
			if err != nil {
				return err
			}
			keys[col.Name] = got
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
	for _, r := range refs {
		if _, ok := keys[r.col.Name][r.key]; !ok {
			return fmt.Errorf("%s refers to %s, which the definition hasn't got", r.where, r.ref)
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

// childRef is a reference from one of a definition's children, or nested
// children, to one of its children, by the key of the collection that holds
// it.
type childRef struct {
	where string
	ref   model.Reference
	col   *model.Collection
	key   string
}

// checkChildren checks a definition's children in collection col of a
// parent of type parent, within the definition, of type root: each of the
// collection's type, setting only fields and nested collections its spec
// describes, but not the field that refers to its parent; each field of its
// kind, or null where it takes it; its key set and different from every
// other child's of the parent, as NetBox would store it; every reference to
// another child of the root given by its collection's key alone, as NetBox
// would store it, which it adds to refs; and its nested collections checked
// the same way. at names the parent in errors, or is empty for the root. It
// returns the children's keys. Below the root, TrueUp compares only nested
// collections, whose children NetBox gives with their parent, and it
// doesn't compare a child's references to objects that stand on their own
// yet.
func (ix *index) checkChildren(root, parent *model.Spec, col *model.Collection, v model.Value, at string, refs *[]childRef) (map[string]int, error) {
	children, ok := v.(model.Children)
	cs := ix.specs[col.Type]
	name := at + col.Name
	switch {
	case !ok:
		return nil, fmt.Errorf("sets %s to %s, not children", name, describe(v))
	case cs == nil:
		return nil, fmt.Errorf("has %s, whose type %s has no spec", name, col.Type)
	case at != "" && !col.Nested:
		return nil, fmt.Errorf("has %s, a collection of a %s's with an endpoint of its own, which TrueUp doesn't compare", name, parent.Type)
	}
	keys := map[string]int{}
	for i, c := range children {
		where := name + "[" + strconv.Itoa(i) + "]"
		if c.Type != col.Type {
			return nil, fmt.Errorf("%s is a %s, not a %s", where, strconv.Quote(c.Type), col.Type)
		}
		for _, field := range slices.Sorted(maps.Keys(c.Fields)) {
			f, v := cs.Field(field), c.Fields[field]
			if nested := cs.Collection(field); f == nil && nested != nil {
				if _, err := ix.checkChildren(root, cs, nested, v, where+".", refs); err != nil {
					return nil, err
				}
				continue
			}
			switch {
			case f == nil:
				return nil, fmt.Errorf("%s sets %s, which the spec of %s doesn't describe", where, strconv.Quote(field), cs.Type)
			case field == col.Parent:
				return nil, fmt.Errorf("%s sets %s, which refers to its parent", where, field)
			case v == nil:
				return nil, fmt.Errorf("%s sets %s to no value", where, field)
			}
			if err := checkKind(f, v); err != nil {
				return nil, fmt.Errorf("%s %s %w", where, field, err)
			}
			ref, isRef := v.(model.Reference)
			switch {
			case !isRef:
				continue
			case f.Ref.Within != root.Type:
				return nil, fmt.Errorf("%s %s refers to a %s outside the %s, which TrueUp doesn't compare in children yet", where, field, ref.Type, root.Type)
			case ref.ID != 0 || len(ref.Key) == 0:
				return nil, fmt.Errorf("%s %s refers to a %s other than by its key", where, field, ref.Type)
			}
			r, err := ix.childRef(root, ref)
			if err != nil {
				return nil, fmt.Errorf("%s %s %w", where, field, err)
			}
			r.where = where + " " + field
			*refs = append(*refs, r)
		}
		key, _, ok := keyOf(col, normalized(cs, c.Fields, col.Key))
		if !ok {
			return nil, fmt.Errorf("%s hasn't got its key, %s", where, strings.Join(col.Key, " and "))
		}
		if j, seen := keys[key]; seen {
			return nil, fmt.Errorf("%s has the key of %s[%d], which NetBox keeps unique among a %s's %s", where, name, j, parent.Type, col.Name)
		}
		keys[key] = i
	}
	return keys, nil
}

// childRef returns, for a reference from a child of an object of type s,
// or from a nested child of one, to another of its children, the
// collection that holds what it refers to, and its key there. The key must
// be the collection's, as NetBox would store it: NetBox's import finds a
// template by its name as the file gives it, so a name NetBox would store
// otherwise names nothing it could hold.
func (ix *index) childRef(s *model.Spec, ref model.Reference) (childRef, error) {
	cs := ix.specs[ref.Type]
	for i := range s.Children {
		col := &s.Children[i]
		if col.Type != ref.Type || cs == nil {
			continue
		}
		if len(ref.Key) != len(col.Key) {
			return childRef{}, fmt.Errorf("refers to a %s by a key of %d fields, but the %s's is %s", ref.Type, len(ref.Key), col.Name, strings.Join(col.Key, " and "))
		}
		key, _, ok := keyOf(col, ref.Key)
		if !ok {
			return childRef{}, fmt.Errorf("refers to a %s without its %s", ref.Type, strings.Join(col.Key, " and "))
		}
		if want, _, _ := keyOf(col, normalized(cs, ref.Key, col.Key)); want != key {
			return childRef{}, fmt.Errorf("refers to %s, by a key NetBox would store otherwise", ref)
		}
		return childRef{ref: ref, col: col, key: key}, nil
	}
	return childRef{}, fmt.Errorf("refers to a %s, which no collection of the %s holds", ref.Type, s.Type)
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
