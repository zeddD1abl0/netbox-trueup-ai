package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
)

// UnmarshalFields reads an object's fields from JSON as TrueUp writes them
// (json.go): the inverse of encoding its Fields with encoding/json. Each
// field is read as its spec says, and each child collection as a list of
// its children's fields, recursively. A field the JSON leaves out is left
// unset, and null is Null, whatever the field: what a definition holds is
// read back as it was, and checked elsewhere. A reference is read as the
// natural key of what it refers to, with its ID when it has one. A field s
// doesn't describe is an error, and so is a value of another shape than the
// field's kind. specs are the specs of every type, from which a reference
// takes the fields of its key.
//
// TrueUp writes this JSON itself, such as the definitions it keeps in its
// database, so UnmarshalFields reads only what MarshalJSON writes. It
// doesn't normalize or validate values: Field.Normalize and Field.Validate
// do.
func (s *Spec) UnmarshalFields(specs []*Spec, raw []byte) (map[string]Value, error) {
	if !json.Valid(raw) {
		return nil, fmt.Errorf("a %s isn't valid JSON", s.Type)
	}
	var in map[string]json.RawMessage
	if kind := jsonKind(raw); kind != '{' || json.Unmarshal(raw, &in) != nil {
		return nil, fmt.Errorf("a %s is %s, not an object", s.Type, describeJSON(kind))
	}
	fields := make(map[string]Value, len(in))
	// In order, so that the same JSON always gives the same error.
	for _, name := range slices.Sorted(maps.Keys(in)) {
		v, err := s.unmarshalField(specs, name, in[name])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		fields[name] = v
	}
	return fields, nil
}

// unmarshalField reads the field or child collection called name.
func (s *Spec) unmarshalField(specs []*Spec, name string, raw json.RawMessage) (Value, error) {
	if f := s.Field(name); f != nil {
		return f.unmarshalValue(specs, raw)
	}
	if col := s.Collection(name); col != nil {
		return unmarshalChildren(specs, col, raw)
	}
	return nil, fmt.Errorf("isn't a field of a %s", s.Type)
}

// unmarshalChildren reads a child collection: a list of each child's
// fields.
func unmarshalChildren(specs []*Spec, col *Collection, raw json.RawMessage) (Children, error) {
	cs := SpecOf(specs, col.Type)
	if cs == nil {
		return nil, fmt.Errorf("holds a %s, which has no spec", col.Type)
	}
	var items []json.RawMessage
	if kind := jsonKind(raw); kind != '[' || json.Unmarshal(raw, &items) != nil {
		return nil, fmt.Errorf("is %s, not a list", describeJSON(kind))
	}
	children := make(Children, 0, len(items))
	for i, item := range items {
		fields, err := cs.UnmarshalFields(specs, item)
		if err != nil {
			return nil, fmt.Errorf("[%d]: %w", i, err)
		}
		children = append(children, Object{Type: cs.Type, Fields: fields})
	}
	return children, nil
}

// unmarshalValue reads one JSON value as field f's, as MarshalJSON writes
// it.
func (f *Field) unmarshalValue(specs []*Spec, raw json.RawMessage) (Value, error) {
	kind := jsonKind(raw)
	if kind == 'n' {
		return Null{}, nil
	}
	switch f.Kind {
	case KindString:
		s, err := jsonString(raw, kind)
		return String(s), err
	case KindChoice:
		s, err := jsonString(raw, kind)
		return Choice(s), err
	case KindDate:
		s, err := jsonString(raw, kind)
		if err != nil {
			return nil, err
		}
		return ParseDate(s)
	case KindBoolean:
		var b bool
		if kind != 'b' || json.Unmarshal(raw, &b) != nil {
			return nil, fmt.Errorf("is %s, not true or false", describeJSON(kind))
		}
		return Boolean(b), nil
	case KindInteger:
		if kind != '0' {
			return nil, fmt.Errorf("is %s, not a number", describeJSON(kind))
		}
		i, err := strconv.ParseInt(string(bytes.TrimSpace(raw)), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%s isn't a whole number TrueUp can hold", quoteShort(string(raw)))
		}
		return Integer(i), nil
	case KindDecimal:
		if kind != '0' {
			return nil, fmt.Errorf("is %s, not a number", describeJSON(kind))
		}
		return ParseDecimal(string(bytes.TrimSpace(raw)))
	case KindReference:
		return f.unmarshalReference(specs, raw, kind)
	case KindSet:
		var elems []json.RawMessage
		if f.Elem == nil || kind != '[' || json.Unmarshal(raw, &elems) != nil {
			return nil, fmt.Errorf("is %s, not a list", describeJSON(kind))
		}
		set := make(Set, 0, len(elems))
		for i, e := range elems {
			v, err := f.Elem.unmarshalValue(specs, e)
			if err != nil {
				return nil, fmt.Errorf("element %d %w", i, err)
			}
			set = append(set, v)
		}
		return set, nil
	case KindChildren, KindNull:
		// A collection, not a field, holds children, and no field is of
		// Null's kind.
	}
	return nil, fmt.Errorf("is %s, which TrueUp doesn't read back", f.Kind.withArticle())
}

// unmarshalReference reads a reference as Reference.MarshalJSON writes it:
// an object of its natural key, with "id" once it is resolved. The JSON
// doesn't say what type the reference is to, so the field must refer to one
// type alone.
func (f *Field) unmarshalReference(specs []*Spec, raw json.RawMessage, kind byte) (Value, error) {
	if f.Ref == nil || len(f.Ref.Types) != 1 {
		return nil, errors.New("is a reference whose type TrueUp can't tell from its JSON")
	}
	typ := f.Ref.Types[0]
	var in map[string]json.RawMessage
	if kind != '{' || json.Unmarshal(raw, &in) != nil {
		return nil, fmt.Errorf("is %s, not a reference to a %s", describeJSON(kind), typ)
	}
	ref := Reference{Type: typ}
	if rawID, ok := in["id"]; ok {
		id, err := positiveID(rawID)
		if err != nil {
			return nil, fmt.Errorf("its id %w", err)
		}
		ref.ID = id
		// A resolved reference that NetBox gives by its ID alone, such as
		// a front port mapping's rear port, has no key.
		if len(in) == 1 {
			return ref, nil
		}
	}
	keySpec, key, err := referenceKey(specs, f.Ref, typ)
	if err != nil {
		return nil, err
	}
	for _, name := range slices.Sorted(maps.Keys(in)) {
		if name != "id" && !slices.Contains(key, name) {
			return nil, fmt.Errorf("names a %s by %s, which isn't part of its key", typ, quoteShort(name))
		}
	}
	ref.Key = make(map[string]Value, len(key))
	for _, name := range key {
		v, ok := in[name]
		if !ok {
			return nil, fmt.Errorf("names a %s without its %s", typ, name)
		}
		if ref.Key[name], err = keySpec.Field(name).unmarshalValue(specs, v); err != nil {
			return nil, fmt.Errorf("%s %w", name, err)
		}
	}
	return ref, nil
}

// referenceKey returns the spec of type typ, which r refers to, and the
// fields of the key that a reference names its object by: its identity, or
// for a reference within a parent (Ref.Within), the key of the parent's
// collection that holds it.
func referenceKey(specs []*Spec, r *Ref, typ string) (*Spec, []string, error) {
	target := SpecOf(specs, typ)
	if target == nil {
		return nil, nil, fmt.Errorf("refers to a %s, which has no spec", typ)
	}
	key := target.Identity
	if r.Within != "" {
		parent := SpecOf(specs, r.Within)
		if parent == nil {
			return nil, nil, fmt.Errorf("refers within a %s, which has no spec", r.Within)
		}
		i := slices.IndexFunc(parent.Children, func(col Collection) bool { return col.Type == typ })
		if i < 0 {
			return nil, nil, fmt.Errorf("refers to a %s within a %s, which has no collection of them", typ, r.Within)
		}
		key = parent.Children[i].Key
	}
	if len(key) == 0 {
		return nil, nil, fmt.Errorf("refers to a %s, which has no key to name it by", typ)
	}
	for _, name := range key {
		if target.Field(name) == nil {
			return nil, nil, fmt.Errorf("refers to a %s by %s, which isn't one of its fields", typ, name)
		}
	}
	return target, key, nil
}
