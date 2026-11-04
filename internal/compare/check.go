package compare

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"

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
// alone, and that it sets its identity's fields, as its Identity gives them.
func checkDefinition(s *model.Spec, d *Definition) error {
	if err := described(s, d.Object.Fields); err != nil {
		return err
	}
	for _, name := range slices.Sorted(maps.Keys(d.Object.Fields)) {
		f, v := s.Field(name), d.Object.Fields[name]
		if v == nil {
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
