// Package model is TrueUp's object model: generic objects, the values of
// their fields, and the specs that describe each NetBox object type
// (docs/adr/0008-describe-object-types-with-specs.md). It does no I/O.
//
// An Object is an object type and its field values. What each field means,
// NetBox's rules for it and how its values compare live in the type's Spec,
// so that comparison, overrides, planning and the UI work the same way for
// every object type.
package model

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// Object is one NetBox object, as a definition describes it or as NetBox
// holds it: an object type and the values of its fields. A field that isn't
// in Fields isn't set, which for a definition means it doesn't manage that
// field. A field that is null holds Null.
type Object struct {
	// Type is the object type, such as dcim.devicetype.
	Type   string
	Fields map[string]Value
}

// Kind is the kind of value a field holds.
type Kind uint8

// The kinds of value. KindNull is the kind of Null, which any nullable field
// can hold, and KindChildren that of a child collection, which a Collection
// declares rather than a Field.
const (
	KindString Kind = iota + 1
	KindInteger
	KindDecimal
	KindBoolean
	KindDate
	KindChoice
	KindReference
	KindSet
	KindChildren
	KindNull
)

var kindNames = map[Kind]string{
	KindString:    "string",
	KindInteger:   "integer",
	KindDecimal:   "decimal",
	KindBoolean:   "boolean",
	KindDate:      "date",
	KindChoice:    "choice",
	KindReference: "reference",
	KindSet:       "set",
	KindChildren:  "children",
	KindNull:      "null",
}

func (k Kind) String() string {
	if name, ok := kindNames[k]; ok {
		return name
	}
	return "Kind(" + strconv.Itoa(int(k)) + ")"
}

// withArticle is the kind's name after "a" or "an", as it reads.
func (k Kind) withArticle() string {
	if k == KindInteger {
		return "an " + k.String()
	}
	return "a " + k.String()
}

// Value is the value of one field: a String, Integer, Decimal, Boolean,
// Date, Choice, Reference, Set, Children or Null.
type Value interface {
	Kind() Kind
	// String describes the value for people, such as in an error.
	String() string
	value()
}

// String is a string field's value.
type String string

// Integer is an integer field's value.
type Integer int64

// Boolean is a boolean field's value.
type Boolean bool

// Choice is the value of a choice field, as NetBox stores it, such as
// front-to-rear. The empty Choice is the blank choice.
type Choice string

// Null is the value of a field that is null.
type Null struct{}

// Reference refers to another object: by NetBox ID once it is resolved, and
// before that by its natural key.
type Reference struct {
	// Type is the object type referred to, such as dcim.manufacturer.
	Type string
	// ID is the object's NetBox ID, or 0 until the reference is resolved.
	ID int64
	// Key is the object's natural key: the values of its spec's identity
	// fields, or for a reference to another child of the same parent
	// (Ref.Within), of the key of the collection that holds it.
	Key map[string]Value
}

// Set is the value of a set field, such as tags. Its order doesn't matter.
type Set []Value

// Children is the value of a child collection: the objects that belong to
// one parent, such as a device type's interface templates. Their order
// doesn't matter.
type Children []Object

func (String) Kind() Kind    { return KindString }
func (Integer) Kind() Kind   { return KindInteger }
func (Boolean) Kind() Kind   { return KindBoolean }
func (Choice) Kind() Kind    { return KindChoice }
func (Null) Kind() Kind      { return KindNull }
func (Reference) Kind() Kind { return KindReference }
func (Set) Kind() Kind       { return KindSet }
func (Children) Kind() Kind  { return KindChildren }

func (String) value()    {}
func (Integer) value()   {}
func (Boolean) value()   {}
func (Choice) value()    {}
func (Null) value()      {}
func (Reference) value() {}
func (Set) value()       {}
func (Children) value()  {}

func (s String) String() string  { return strconv.Quote(string(s)) }
func (i Integer) String() string { return strconv.FormatInt(int64(i), 10) }
func (b Boolean) String() string { return strconv.FormatBool(bool(b)) }
func (c Choice) String() string  { return strconv.Quote(string(c)) }
func (Null) String() string      { return "null" }

func (r Reference) String() string {
	var b strings.Builder
	b.WriteString(r.Type)
	if r.ID != 0 {
		fmt.Fprintf(&b, " #%d", r.ID)
	}
	for _, name := range slices.Sorted(maps.Keys(r.Key)) {
		fmt.Fprintf(&b, " %s=%s", name, describe(r.Key[name]))
	}
	return b.String()
}

func (s Set) String() string {
	parts := make([]string, len(s))
	for i, v := range s {
		parts[i] = describe(v)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func (c Children) String() string {
	return fmt.Sprintf("%d children", len(c))
}

// describe is v.String(), or "unset" for nil.
func describe(v Value) string {
	if v == nil {
		return "unset"
	}
	return v.String()
}

// Equal reports whether a and b are the same value: of the same kind, with
// decimals compared as numbers and sets and children in any order. Two
// references are equal when they refer to the same type and, if both are
// resolved, to the same ID, or otherwise by the same key. Values compare as
// they are: normalize them first (Field.Normalize) to compare them as NetBox
// would. nil, an unset field, equals only nil.
func Equal(a, b Value) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if a.Kind() != b.Kind() {
		return false
	}
	switch a := a.(type) {
	case Reference:
		b, ok := b.(Reference)
		switch {
		case !ok || a.Type != b.Type:
			return false
		case a.ID != 0 && b.ID != 0:
			return a.ID == b.ID
		}
		return equalFields(a.Key, b.Key)
	case Set:
		b, ok := b.(Set)
		return ok && sameElements(a, b, Equal)
	case Children:
		b, ok := b.(Children)
		return ok && sameElements(a, b, Object.Equal)
	default:
		// The other kinds are comparable, Decimal and Date included, since
		// a Decimal is always in its lowest terms.
		return a == b
	}
}

// Equal reports whether o and other have the same type and equal fields.
func (o Object) Equal(other Object) bool {
	return o.Type == other.Type && equalFields(o.Fields, other.Fields)
}

func equalFields(a, b map[string]Value) bool {
	if len(a) != len(b) {
		return false
	}
	for name, v := range a {
		w, ok := b[name]
		if !ok || !Equal(v, w) {
			return false
		}
	}
	return true
}

// sameElements reports whether a and b hold equal elements, as many times
// each, in any order: whether every element of a can be paired with its own
// equal element of b. Equality between references isn't transitive, since
// two resolved references compare by ID and others by key, so a greedy
// pairing can miss one that exists; sameElements finds a pairing by
// augmenting paths when the greedy one fails.
func sameElements[T any](a, b []T, equal func(T, T) bool) bool {
	if len(a) != len(b) {
		return false
	}
	pairedWith := make([]int, len(b)) // index in a + 1, or 0
	greedy := true
	for i, x := range a {
		found := false
		for j, y := range b {
			if pairedWith[j] == 0 && equal(x, y) {
				pairedWith[j], found = i+1, true
				break
			}
		}
		if !found {
			// Most often x has no equal at all, and a and b just differ.
			if !slices.ContainsFunc(b, func(y T) bool { return equal(x, y) }) {
				return false
			}
			greedy = false
			break
		}
	}
	if greedy {
		return true
	}
	// Kuhn's algorithm over the equality graph.
	eq := make([][]bool, len(a))
	for i, x := range a {
		eq[i] = make([]bool, len(b))
		for j, y := range b {
			eq[i][j] = equal(x, y)
		}
	}
	clear(pairedWith)
	var augment func(i int, seen []bool) bool
	augment = func(i int, seen []bool) bool {
		for j := range b {
			if eq[i][j] && !seen[j] {
				seen[j] = true
				if pairedWith[j] == 0 || augment(pairedWith[j]-1, seen) {
					pairedWith[j] = i + 1
					return true
				}
			}
		}
		return false
	}
	for i := range a {
		if !augment(i, make([]bool, len(b))) {
			return false
		}
	}
	return true
}
