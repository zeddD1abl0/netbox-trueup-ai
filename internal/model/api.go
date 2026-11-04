package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
)

// DecodeAPI reads an object as NetBox's REST API returns it, such as one of
// a listing's results, into its NetBox ID and the values of its fields: every
// field s describes, which the answer must hold, read as NetBox 4.7 writes it
// (docs/architecture.md, "Normalization"). Other fields of the answer are
// left out. specs are the specs of every type, from which a reference takes
// the identity of the object it refers to.
//
// What NetBox sends is untrusted (boundary B2 in docs/architecture.md), so
// DecodeAPI checks the shape of every value, and fails on the first that
// isn't as NetBox writes it. It doesn't normalize or validate values:
// Field.Normalize and Field.Validate do.
func (s *Spec) DecodeAPI(specs []*Spec, raw []byte) (int64, Object, error) {
	var answer map[string]json.RawMessage
	if jsonKind(raw) != '{' || json.Unmarshal(raw, &answer) != nil {
		return 0, Object{}, fmt.Errorf("%s: NetBox's answer isn't a JSON object", s.Type)
	}
	id, err := positiveID(answer["id"])
	if err != nil {
		return 0, Object{}, fmt.Errorf("%s: id: %w", s.Type, err)
	}
	obj := Object{Type: s.Type, Fields: make(map[string]Value, len(s.Fields))}
	for i := range s.Fields {
		f := &s.Fields[i]
		v, err := f.decodeAPI(specs, answer)
		if err != nil {
			return 0, Object{}, fmt.Errorf("%s #%d: %s: %w", s.Type, id, f.Name, err)
		}
		obj.Fields[f.Name] = v
	}
	return id, obj, nil
}

// decodeAPI reads field f from an answer.
func (f *Field) decodeAPI(specs []*Spec, answer map[string]json.RawMessage) (Value, error) {
	if f.Kind == KindReference && f.Ref != nil && f.Ref.Shape == ShapeTypeAndID {
		return f.decodeTypeAndID(answer)
	}
	raw, ok := answer[f.Name]
	if !ok {
		return nil, errors.New("isn't in the answer")
	}
	return f.decodeValue(specs, raw)
}

// decodeValue reads one JSON value as field f's.
func (f *Field) decodeValue(specs []*Spec, raw json.RawMessage) (Value, error) {
	kind := jsonKind(raw)
	if kind == 'n' {
		switch {
		case f.Nullable:
			return Null{}, nil
		case f.Kind == KindChoice && f.Blank:
			// NetBox's ChoiceField reads the blank choice as null.
			return Choice(""), nil
		}
		return nil, errors.New("is null, which it can't be")
	}
	switch f.Kind {
	case KindString:
		s, err := jsonString(raw, kind)
		return String(s), err
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
		// NetBox writes decimals as JSON numbers (COERCE_DECIMAL_TO_STRING
		// is off in netbox/settings.py), never as strings.
		if kind != '0' {
			return nil, fmt.Errorf("is %s, not a number", describeJSON(kind))
		}
		return ParseDecimal(string(bytes.TrimSpace(raw)))
	case KindChoice:
		// {"value": ..., "label": ...}, as ChoiceField.to_representation
		// writes it (netbox/api/fields.py). Read through a map, which
		// matches keys exactly, as everywhere else, unlike a struct.
		var choice map[string]json.RawMessage
		if kind != '{' || json.Unmarshal(raw, &choice) != nil {
			return nil, fmt.Errorf("is %s, not a choice's value and label", describeJSON(kind))
		}
		s, err := jsonString(choice["value"], jsonKind(choice["value"]))
		if err != nil {
			return nil, fmt.Errorf("its value %w", err)
		}
		return Choice(s), nil
	case KindReference:
		return f.decodeReference(specs, raw, kind)
	case KindSet:
		var elems []json.RawMessage
		if f.Elem == nil || kind != '[' || json.Unmarshal(raw, &elems) != nil {
			return nil, fmt.Errorf("is %s, not a list", describeJSON(kind))
		}
		set := make(Set, 0, len(elems))
		for i, e := range elems {
			v, err := f.Elem.decodeValue(specs, e)
			if err != nil {
				return nil, fmt.Errorf("element %d %w", i, err)
			}
			set = append(set, v)
		}
		return set, nil
	case KindChildren, KindNull:
		// A collection's children have endpoints of their own, or come in
		// their parent's field, and no field is of Null's kind.
	}
	return nil, fmt.Errorf("is %s, which TrueUp doesn't read from NetBox", f.Kind.withArticle())
}

// decodeReference reads a reference as NetBox writes it: a brief object with
// the ID and the natural key (ShapeNested), or the ID alone (ShapeID).
func (f *Field) decodeReference(specs []*Spec, raw json.RawMessage, kind byte) (Value, error) {
	if f.Ref == nil || len(f.Ref.Types) != 1 {
		return nil, errors.New("is a reference whose type TrueUp can't tell from NetBox's answer")
	}
	typ := f.Ref.Types[0]
	if f.Ref.Shape == ShapeID {
		id, err := positiveID(raw)
		if err != nil {
			return nil, err
		}
		return Reference{Type: typ, ID: id}, nil
	}
	var nested map[string]json.RawMessage
	if kind != '{' || json.Unmarshal(raw, &nested) != nil {
		return nil, fmt.Errorf("is %s, not a reference to a %s", describeJSON(kind), typ)
	}
	id, err := positiveID(nested["id"])
	if err != nil {
		return nil, fmt.Errorf("its id %w", err)
	}
	target := specFor(specs, typ)
	if target == nil {
		return nil, fmt.Errorf("refers to a %s, which has no spec", typ)
	}
	ref := Reference{Type: typ, ID: id, Key: make(map[string]Value, len(target.Identity))}
	for _, name := range target.Identity {
		kf := target.Field(name)
		v, ok := nested[name]
		if kf == nil || !ok {
			return nil, fmt.Errorf("refers to a %s without its %s", typ, name)
		}
		if ref.Key[name], err = kf.decodeValue(specs, v); err != nil {
			return nil, fmt.Errorf("%s %w", name, err)
		}
	}
	return ref, nil
}

// decodeTypeAndID reads a generic reference from the two fields that carry
// it, such as an inventory item template's component_type and component_id:
// both null, or an object type the reference can refer to and an ID.
func (f *Field) decodeTypeAndID(answer map[string]json.RawMessage) (Value, error) {
	rawType, okType := answer[f.Ref.TypeField]
	rawID, okID := answer[f.Ref.IDField]
	if !okType || !okID {
		return nil, fmt.Errorf("%s or %s isn't in the answer", f.Ref.TypeField, f.Ref.IDField)
	}
	if jsonKind(rawType) == 'n' && jsonKind(rawID) == 'n' && f.Nullable {
		return Null{}, nil
	}
	typ, err := jsonString(rawType, jsonKind(rawType))
	if err != nil {
		return nil, fmt.Errorf("%s %w", f.Ref.TypeField, err)
	}
	if !slices.Contains(f.Ref.Types, typ) {
		return nil, fmt.Errorf("%s is %s, which it can't refer to", f.Ref.TypeField, quoteShort(typ))
	}
	id, err := positiveID(rawID)
	if err != nil {
		return nil, fmt.Errorf("%s %w", f.Ref.IDField, err)
	}
	return Reference{Type: typ, ID: id}, nil
}

func specFor(specs []*Spec, typ string) *Spec {
	for _, s := range specs {
		if s.Type == typ {
			return s
		}
	}
	return nil
}

// positiveID reads a NetBox ID: a whole number from 1.
func positiveID(raw json.RawMessage) (int64, error) {
	if raw == nil {
		return 0, errors.New("isn't in the answer")
	}
	kind := jsonKind(raw)
	if kind != '0' {
		return 0, fmt.Errorf("is %s, not an ID", describeJSON(kind))
	}
	id, err := strconv.ParseInt(string(bytes.TrimSpace(raw)), 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("%s isn't an ID", quoteShort(string(raw)))
	}
	return id, nil
}

// jsonString reads a JSON string. encoding/json replaces bytes that aren't
// UTF-8 with U+FFFD, so the string is always UTF-8.
func jsonString(raw json.RawMessage, kind byte) (string, error) {
	var s string
	if kind != '"' || json.Unmarshal(raw, &s) != nil {
		return "", fmt.Errorf("is %s, not text", describeJSON(kind))
	}
	return s, nil
}

// jsonKind returns what a JSON value is, from its first character: '"' for
// a string, '0' for a number, 'b' for true or false, 'n' for null, '{' for
// an object, '[' for a list, or 0 for nothing. Valid JSON is assumed, since
// encoding/json checked the whole answer.
func jsonKind(raw json.RawMessage) byte {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return 0
	}
	switch c := raw[0]; {
	case c == '"', c == '{', c == '[':
		return c
	case c == 't', c == 'f':
		return 'b'
	case c == 'n':
		return 'n'
	case c == '-' || c >= '0' && c <= '9':
		return '0'
	}
	return 0
}

func describeJSON(kind byte) string {
	switch kind {
	case '"':
		return "text"
	case '0':
		return "a number"
	case 'b':
		return "true or false"
	case 'n':
		return "null"
	case '{':
		return "an object"
	case '[':
		return "a list"
	}
	return "missing"
}
