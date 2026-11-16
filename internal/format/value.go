package format

import (
	"fmt"
	"math"
	"strconv"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
)

// Value reads a scalar as the value of field f, by the field's kind: text as
// a string, a choice or a date, a number as a decimal, a whole number as an
// integer or a decimal, a number such as 2.0 as an integer where NetBox's
// import takes it as one, true or false as a boolean, and null as Null. It
// doesn't normalize or validate the value: f.Normalize and f.Validate do. A
// reference names another object, which only a format knows how to read.
func Value(f *model.Field, n *Node) (model.Value, error) {
	if n.Kind != Scalar {
		return nil, fmt.Errorf("is %s, but NetBox's %s is %s", Describe(n), f.Name, kindName(f.Kind))
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
		if n.Tag == TagFloat {
			return wholeFloat(f, n.Value)
		}
	default:
		return nil, fmt.Errorf("TrueUp doesn't read NetBox's %s, %s, from files", f.Name, kindName(f.Kind))
	}
	return nil, mismatch()
}

// pyReprExponent is where Python starts writing a float with an exponent:
// repr(1e16) is '1e+16', and repr(9999999999999998.0) '9999999999999998.0'.
const pyReprExponent = 1e16

// wholeFloat reads a number PyYAML resolved as a float, given in its
// shortest decimal form, as the value of an integer field, as NetBox's
// import reads one: Django's forms.IntegerField.to_python takes str(value),
// strips a trailing decimal point and zeros (re_decimal, \.0*\s*$), and
// passes the rest to int(). So 2.0 is 2, but 2.5 isn't a whole number, and
// 1.0e+16, which Python writes as 1e+16, isn't one int() takes.
func wholeFloat(f *model.Field, v string) (model.Value, error) {
	x, err := strconv.ParseFloat(v, 64)
	switch {
	case err != nil:
		// resolvePlain gives only values that read back.
		return nil, fmt.Errorf("%s isn't a number TrueUp can read", v)
	case x != math.Trunc(x):
		return nil, fmt.Errorf("is %s, but NetBox's %s is a whole number", v, f.Name)
	case math.Abs(x) >= pyReprExponent:
		return nil, fmt.Errorf("is %s written with a decimal point, which NetBox's import refuses for a whole number as large as this: write it without one", v)
	}
	return model.Integer(int64(x)), nil
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
