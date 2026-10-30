package model

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// Normalize returns v as NetBox would hold it in field f, so that values
// NetBox treats as the same compare equal (docs/architecture.md,
// "Normalization"):
//
//   - with f.Trim, a string loses white space at both ends, as Python's
//     str.strip removes it;
//   - with f.BlankIsNull, a blank string or choice becomes Null;
//   - a set's elements are normalized as f.Elem says.
//
// Decimals need nothing, since a Decimal is always in its lowest terms: 1
// and 1.0 are the same Decimal. References compare by ID once resolved, and
// sets and children in any order, which Equal does. Normalize doesn't check
// v against NetBox's rules; Validate does. It leaves a value of another kind
// than f's as it is, for Validate to refuse.
func (f *Field) Normalize(v Value) Value {
	if v == nil || v.Kind() != f.Kind {
		return v
	}
	switch v := v.(type) {
	case String:
		if f.Trim {
			v = String(strings.TrimFunc(string(v), isPythonSpace))
		}
		if v == "" && f.BlankIsNull {
			return Null{}
		}
		return v
	case Choice:
		if v == "" && f.BlankIsNull {
			return Null{}
		}
		return v
	case Set:
		if f.Elem == nil {
			return v
		}
		out := make(Set, len(v))
		for i, e := range v {
			out[i] = f.Elem.Normalize(e)
		}
		return out
	default:
		return v
	}
}

// isPythonSpace reports whether Python's str.strip, which NetBox's string
// fields use to trim input, removes r. Python counts as white space what Go
// does, plus the four information separators U+001C to U+001F, which
// Python's str.isspace includes (checked with NetBox 4.7.2's Python 3.14).
func isPythonSpace(r rune) bool {
	return unicode.IsSpace(r) || r >= 0x1c && r <= 0x1f
}

// ErrConfigurableChoice is returned, wrapped, by Validate for a value that
// isn't among a choice field's values when a NetBox administrator can add
// choices to the field (Choices.Configurable). The NetBox TrueUp talks to may
// take it.
var ErrConfigurableChoice = errors.New("not one of the choices NetBox ships with")

// Validate checks v against NetBox's rules for field f, as NetBox 4.7 checks
// a value written through its API: its kind, null and blank, length,
// pattern, digits and decimal places, bounds, step, choices, and the types a
// reference can refer to. v should be normalized first. Rules that span
// fields, and whether a required field is set, are for the object to check.
func (f *Field) Validate(v Value) error {
	if err := f.validate(v); err != nil {
		return fmt.Errorf("%s: %w", f.Name, err)
	}
	return nil
}

func (f *Field) validate(v Value) error {
	switch {
	case v == nil:
		return errors.New("has no value")
	case v.Kind() == KindNull:
		if !f.Nullable {
			return errors.New("can't be null")
		}
		return nil
	case v.Kind() != f.Kind:
		return fmt.Errorf("is %s, not %s", v.Kind().withArticle(), f.Kind.withArticle())
	}
	switch v := v.(type) {
	case String:
		return f.validateString(string(v))
	case Choice:
		return f.validateChoice(string(v))
	case Decimal:
		return f.validateDecimal(v)
	case Integer:
		return f.validateBounds(v)
	case Reference:
		switch {
		case f.Ref == nil || !slices.Contains(f.Ref.Types, v.Type):
			return fmt.Errorf("refers to a %s, which it can't", quoteShort(v.Type))
		case v.ID == 0 && len(v.Key) == 0:
			return errors.New("refers to nothing: it has neither an ID nor a key")
		}
	case Date:
		if v == (Date{}) {
			return errors.New("isn't a date")
		}
	case Set:
		if f.Elem == nil {
			return errors.New("has no element type")
		}
		for _, e := range v {
			if err := f.Elem.validate(e); err != nil {
				return fmt.Errorf("element %s %w", describe(e), err)
			}
		}
	}
	return nil
}

// validateString checks s as Django REST Framework's CharField does, which
// refuses NUL characters (ProhibitNullCharactersValidator), and as
// encoding/json needs: a string that isn't UTF-8 would reach NetBox with
// U+FFFD in place of its bad bytes, and never compare equal again.
func (f *Field) validateString(s string) error {
	switch {
	case !utf8.ValidString(s):
		return fmt.Errorf("%s isn't UTF-8", quoteShort(s))
	case strings.ContainsRune(s, 0):
		return fmt.Errorf("%s holds a NUL character, which NetBox refuses", quoteShort(s))
	}
	if s == "" {
		if !f.Blank {
			return errors.New("can't be blank")
		}
		return nil
	}
	if n := utf8.RuneCountInString(s); f.MaxLength > 0 && n > f.MaxLength {
		return fmt.Errorf("has %d characters, more than %d", n, f.MaxLength)
	}
	if f.Pattern != "" {
		re, err := compile(f.Pattern)
		if err != nil {
			return err
		}
		if !re.MatchString(s) {
			return fmt.Errorf("%s doesn't match %s", quoteShort(s), f.Pattern)
		}
	}
	return nil
}

func (f *Field) validateChoice(c string) error {
	switch {
	case c == "":
		if !f.Blank {
			return errors.New("can't be blank")
		}
	case f.Choices == nil:
		return errors.New("has no choices")
	case slices.Contains(f.Choices.Values, c):
	case f.Choices.Configurable != "":
		return fmt.Errorf("%s is %w, though an administrator can add choices through FIELD_CHOICES[%q]", quoteShort(c), ErrConfigurableChoice, f.Choices.Configurable)
	default:
		return fmt.Errorf("%s isn't one of its choices", quoteShort(c))
	}
	return nil
}

// validateDecimal checks d as Django REST Framework's DecimalField does: no
// more than Places decimal places and Digits digits in all, of which no more
// than Digits-Places before the decimal point. It refuses a value with too
// many decimal places rather than round it.
func (f *Field) validateDecimal(d Decimal) error {
	switch {
	case d.Places() > f.Places:
		return fmt.Errorf("%s has more than %d decimal places", d, f.Places)
	case d.Digits() > f.Digits:
		return fmt.Errorf("%s has more than %d digits", d, f.Digits)
	case d.Digits()-d.Places() > f.Digits-f.Places:
		return fmt.Errorf("%s has more than %d digits before the decimal point", d, f.Digits-f.Places)
	}
	if step, ok := f.Step.(Decimal); ok && !d.MultipleOf(step) {
		return fmt.Errorf("%s isn't a multiple of %s", d, step)
	}
	return f.validateBounds(d)
}

func (f *Field) validateBounds(v Value) error {
	if f.Min != nil && compare(v, f.Min) < 0 {
		return fmt.Errorf("%s is less than %s", v, f.Min)
	}
	if f.Max != nil && compare(v, f.Max) > 0 {
		return fmt.Errorf("%s is more than %s", v, f.Max)
	}
	return nil
}

// compare compares two Integers or Decimals as numbers. Check makes sure a
// field's bounds are of the field's kind.
func compare(a, b Value) int {
	return asDecimal(a).Cmp(asDecimal(b))
}

func asDecimal(v Value) Decimal {
	switch v := v.(type) {
	case Decimal:
		return v
	case Integer:
		return NewDecimal(int64(v), 0)
	}
	return Decimal{}
}

// patterns caches compiled patterns, which specs share.
var patterns sync.Map // pattern string -> *regexp.Regexp

// compile compiles a field's pattern, anchored to match whole strings.
func compile(pattern string) (*regexp.Regexp, error) {
	if v, ok := patterns.Load(pattern); ok {
		if re, ok := v.(*regexp.Regexp); ok {
			return re, nil
		}
	}
	re, err := regexp.Compile(`\A(?:` + pattern + `)\z`)
	if err != nil {
		return nil, fmt.Errorf("pattern %q: %w", pattern, err)
	}
	patterns.Store(pattern, re)
	return re, nil
}
