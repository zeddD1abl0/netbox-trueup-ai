package format

import (
	"errors"
	"math"
	"math/big"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Tag is the type of a YAML scalar, as PyYAML's SafeLoader resolves it.
type Tag uint8

// Tags. A scalar of any other type, such as a timestamp, is refused.
const (
	TagStr Tag = iota + 1
	TagInt
	TagFloat
	TagBool
	TagNull
)

var tagNames = map[Tag]string{
	TagStr:   "text",
	TagInt:   "a whole number",
	TagFloat: "a number",
	TagBool:  "true or false",
	TagNull:  "null",
}

func (t Tag) String() string {
	if name, ok := tagNames[t]; ok {
		return name
	}
	return "Tag(" + strconv.Itoa(int(t)) + ")"
}

// PyYAML 6's implicit resolvers, from yaml/resolver.py, which NetBox's YAML
// import (BulkImportForm._clean_yaml in utilities/forms/bulk_import.py, with
// yaml.SafeLoader) and devicetype-library's own tests (tests/yaml_loader.py)
// use. They are tried in this order, and the first that matches decides.
// PyYAML's ^ and $ become \A and \z: a plain scalar never ends with a line
// break, before which Python's $ also matches.
var (
	pyBool  = regexp.MustCompile(`\A(?:yes|Yes|YES|no|No|NO|true|True|TRUE|false|False|FALSE|on|On|ON|off|Off|OFF)\z`)
	pyFloat = regexp.MustCompile(`\A(?:[-+]?(?:[0-9][0-9_]*)\.[0-9_]*(?:[eE][-+][0-9]+)?` +
		`|\.[0-9][0-9_]*(?:[eE][-+][0-9]+)?` +
		`|[-+]?[0-9][0-9_]*(?::[0-5]?[0-9])+\.[0-9_]*` +
		`|[-+]?\.(?:inf|Inf|INF)` +
		`|\.(?:nan|NaN|NAN))\z`)
	pyInt = regexp.MustCompile(`\A(?:[-+]?0b[0-1_]+` +
		`|[-+]?0[0-7_]+` +
		`|[-+]?(?:0|[1-9][0-9_]*)` +
		`|[-+]?0x[0-9a-fA-F_]+` +
		`|[-+]?[1-9][0-9_]*(?::[0-5]?[0-9])+)\z`)
	pyMerge     = regexp.MustCompile(`\A(?:<<)\z`)
	pyNull      = regexp.MustCompile(`\A(?:~|null|Null|NULL|)\z`)
	pyTimestamp = regexp.MustCompile(`\A(?:[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]` +
		`|[0-9][0-9][0-9][0-9]-[0-9][0-9]?-[0-9][0-9]?` +
		`(?:[Tt]|[ \t]+)[0-9][0-9]?` +
		`:[0-9][0-9]:[0-9][0-9](?:\.[0-9]*)?` +
		`(?:[ \t]*(?:Z|[-+][0-9][0-9]?(?::[0-9][0-9])?))?)\z`)
	pyValue = regexp.MustCompile(`\A(?:=)\z`)
)

// resolvePlain returns what PyYAML's SafeLoader makes of a plain scalar, one
// written without quotes, a block indicator or a tag: its tag, and its value
// in a canonical form. A string's value is the text itself; a whole number's
// its decimal digits; a number's the shortest decimal that reads back as the
// same binary float, as Python writes a float and as NetBox's DecimalField
// and devicetype-library's DecimalSafeLoader read one; a boolean's "true" or
// "false"; and null's "".
//
// It returns an error for what has no value in JSON or in NetBox's fields:
// a date or time, infinity, not a number, a whole number too large for 64
// bits, and PyYAML's merge key and value indicator outside where SafeLoader
// takes them.
func resolvePlain(s string) (Tag, string, error) {
	switch {
	case pyBool.MatchString(s):
		switch strings.ToLower(s) {
		case "yes", "true", "on":
			return TagBool, "true", nil
		default:
			return TagBool, "false", nil
		}
	case pyFloat.MatchString(s):
		v, err := pyFloatValue(s)
		if err != nil {
			return 0, "", err
		}
		return TagFloat, v, nil
	case pyInt.MatchString(s):
		v, err := pyIntValue(s)
		if err != nil {
			return 0, "", err
		}
		return TagInt, v, nil
	case pyMerge.MatchString(s):
		return 0, "", errors.New("<< is YAML's merge key, which this reader doesn't take: quote it if it is text")
	case pyNull.MatchString(s):
		return TagNull, "", nil
	case pyTimestamp.MatchString(s):
		return 0, "", errors.New(Quote(s) + " reads as a date or time, which the format doesn't take: quote it if it is text")
	case pyValue.MatchString(s):
		return 0, "", errors.New("= is YAML's value indicator, which PyYAML refuses: quote it if it is text")
	}
	return TagStr, s, nil
}

// pyIntValue reads a whole number as SafeConstructor.construct_yaml_int
// does: underscores dropped, then binary after 0b, hexadecimal after 0x,
// octal after a leading 0, base 60 between colons, and decimal otherwise.
func pyIntValue(s string) (string, error) {
	v := strings.ReplaceAll(s, "_", "")
	negative := false
	if v[0] == '-' || v[0] == '+' {
		negative = v[0] == '-'
		v = v[1:]
	}
	n := new(big.Int)
	ok := true
	switch {
	case v == "0":
	case strings.HasPrefix(v, "0b"):
		_, ok = n.SetString(v[2:], 2)
	case strings.HasPrefix(v, "0x"):
		_, ok = n.SetString(v[2:], 16)
	case v[0] == '0':
		_, ok = n.SetString(v, 8)
	case strings.Contains(v, ":"):
		base := big.NewInt(1)
		for _, part := range slices.Backward(strings.Split(v, ":")) {
			digit, good := new(big.Int).SetString(part, 10)
			if !good {
				ok = false
				break
			}
			n.Add(n, digit.Mul(digit, base))
			base.Mul(base, big.NewInt(60))
		}
	default:
		_, ok = n.SetString(v, 10)
	}
	if !ok {
		// Such as 0b_, whose digits are only underscores. PyYAML fails
		// on it too.
		return "", errors.New(Quote(s) + " reads as a whole number, but isn't one")
	}
	if negative {
		n.Neg(n)
	}
	if !n.IsInt64() {
		return "", errors.New(Quote(s) + " is a whole number too large for TrueUp")
	}
	return n.String(), nil
}

// pyFloatValue reads a number as SafeConstructor.construct_yaml_float
// does, into a binary float, and returns its shortest decimal form, as
// Python's repr gives it and Go's strconv.FormatFloat with precision -1
// does too, though Go writes an exponent from 1e21 where Python does from
// 1e16. Both are the same decimal.
func pyFloatValue(s string) (string, error) {
	v := strings.ToLower(strings.ReplaceAll(s, "_", ""))
	sign := 1.0
	if v[0] == '-' || v[0] == '+' {
		if v[0] == '-' {
			sign = -1
		}
		v = v[1:]
	}
	var f float64
	switch {
	case v == ".inf", v == ".nan":
		return "", errors.New(Quote(s) + " isn't a number NetBox can hold")
	case strings.Contains(v, ":"):
		// Base 60, as construct_yaml_float adds it up, in floats.
		base := 1.0
		for _, part := range slices.Backward(strings.Split(v, ":")) {
			digit, err := strconv.ParseFloat(part, 64)
			if err != nil {
				return "", errors.New(Quote(s) + " reads as a number, but isn't one")
			}
			f += digit * base
			base *= 60
		}
	default:
		var err error
		f, err = strconv.ParseFloat(v, 64)
		// Python reads a number too large for a float as infinity, which
		// NetBox then refuses.
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			return "", errors.New(Quote(s) + " reads as a number, but isn't one")
		}
	}
	f *= sign
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return "", errors.New(Quote(s) + " is too large a number for NetBox")
	}
	return strconv.FormatFloat(f, 'g', -1, 64), nil
}
