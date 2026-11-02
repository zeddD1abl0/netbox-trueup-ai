package format

import (
	"fmt"
	"strconv"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
)

// Value reads a scalar as the value of field f, by the field's kind: text as
// a string, a choice or a date, a number as a decimal, a whole number as an
// integer or a decimal, true or false as a boolean, and null as Null. It
// doesn't normalize or validate the value: f.Normalize and f.Validate do. A
// reference names another object, which only a format knows how to read.
func Value(f *model.Field, n *Node) (model.Value, error) {
	if n.Kind != Scalar {
		return nil, fmt.Errorf("is %s, but NetBox's %s is %s", describeNode(n), f.Name, kindName(f.Kind))
	}
	if n.Tag == TagNull {
		return model.Null{}, nil
	}
	mismatch := func() error {
		return fmt.Errorf("is %s, but NetBox's %s is %s", n.Tag, f.Name, kindName(f.Kind))
	}
	switch f.Kind {
	case model.KindString:
		if n.Tag == TagStr {
			return model.String(n.Value), nil
		}
	case model.KindChoice:
		if n.Tag == TagStr {
			return model.Choice(n.Value), nil
		}
	case model.KindDate:
		if n.Tag == TagStr {
			return model.ParseDate(n.Value)
		}
	case model.KindBoolean:
		if n.Tag == TagBool {
			return model.Boolean(n.Value == "true"), nil
		}
	case model.KindDecimal:
		if n.Tag == TagInt || n.Tag == TagFloat {
			return model.ParseDecimal(n.Value)
		}
	case model.KindInteger:
		if n.Tag == TagInt {
			i, err := strconv.ParseInt(n.Value, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("%s isn't a whole number TrueUp can hold", n.Value)
			}
			return model.Integer(i), nil
		}
	default:
		return nil, fmt.Errorf("TrueUp doesn't read NetBox's %s, %s, from files", f.Name, kindName(f.Kind))
	}
	return nil, mismatch()
}

func kindName(k model.Kind) string {
	switch k {
	case model.KindString:
		return "text"
	case model.KindChoice:
		return "a choice, given as text"
	case model.KindDate:
		return "a date, given as text such as '2027-03-31'"
	case model.KindBoolean:
		return "true or false"
	case model.KindDecimal:
		return "a number"
	case model.KindInteger:
		return "a whole number"
	case model.KindReference:
		return "a reference to another object"
	default:
		return k.String()
	}
}

// Quote quotes a value from a file for a problem's reason: cut short, and
// with anything that isn't printable escaped.
func Quote(s string) string {
	return quote(s)
}

// JoinField adds a key to a field path, such as interfaces[3] and name to
// make interfaces[3].name, quoting a key that isn't plain letters, digits,
// - and _.
func JoinField(field, key string) string {
	return join(field, key)
}

// Describe says what a node is, for a problem's reason: a mapping, a list,
// text, a whole number, and so on.
func Describe(n *Node) string {
	return describeNode(n)
}
