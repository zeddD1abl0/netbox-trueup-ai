package model

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

// Decimal is an exact decimal number, as NetBox's DecimalFields hold: an
// integer coefficient divided by a power of ten. It is always in its lowest
// terms, with no trailing zeros after the decimal point, so 1.0 and 1 are
// the same Decimal and == compares Decimals as numbers. The zero value is 0.
//
// NetBox 4.7.2's widest DecimalField has 16 digits (max_digits), within the
// 18 that ParseDecimal reads into the int64 coefficient.
type Decimal struct {
	coef  int64
	scale uint8 // digits after the decimal point
}

// maxDecimalDigits is the most significant digits, and the most decimal
// places, that ParseDecimal reads.
const maxDecimalDigits = 18

// NewDecimal returns coefficient divided by 10 to the power places: for
// example, NewDecimal(15, 1) is 1.5.
func NewDecimal(coefficient int64, places uint8) Decimal {
	d := Decimal{coef: coefficient, scale: places}
	for d.scale > 0 && d.coef%10 == 0 {
		d.coef /= 10
		d.scale--
	}
	if d.coef == 0 {
		d.scale = 0
	}
	return d
}

// ParseDecimal reads a decimal number written as JSON and YAML write
// numbers: an optional sign, digits with an optional fraction, and an
// optional exponent, such as 1, -0.5, 12.30 or 1e-06. NetBox's API writes
// decimals that way, as Python writes floats. It refuses a number with more
// than 18 significant digits or decimal places, rather than round it.
func ParseDecimal(s string) (Decimal, error) {
	rest := s
	negative := false
	if rest != "" && (rest[0] == '-' || rest[0] == '+') {
		negative = rest[0] == '-'
		rest = rest[1:]
	}
	exponent := 0
	if i := strings.IndexAny(rest, "eE"); i >= 0 {
		e, err := strconv.Atoi(rest[i+1:])
		if err != nil || rest[i+1:] == "" || e < -1000 || e > 1000 {
			return Decimal{}, fmt.Errorf("%s isn't a decimal number", quoteShort(s))
		}
		exponent, rest = e, rest[:i]
	}
	whole, fraction, _ := strings.Cut(rest, ".")
	if whole == "" && fraction == "" || !allDigits(whole) || !allDigits(fraction) {
		return Decimal{}, fmt.Errorf("%s isn't a decimal number", quoteShort(s))
	}

	digits := strings.TrimLeft(whole+fraction, "0")
	scale := len(fraction) - exponent
	for scale > 0 && strings.HasSuffix(digits, "0") {
		digits = digits[:len(digits)-1]
		scale--
	}
	switch {
	case digits == "":
		return Decimal{}, nil
	case len(digits)-min(scale, 0) > maxDecimalDigits:
		return Decimal{}, fmt.Errorf("%s has more than %d significant digits", quoteShort(s), maxDecimalDigits)
	case scale > maxDecimalDigits:
		return Decimal{}, fmt.Errorf("%s has more than %d decimal places", quoteShort(s), maxDecimalDigits)
	case scale < 0:
		digits += strings.Repeat("0", -scale)
		scale = 0
	}
	// 18 digits always fit in an int64, so this can't fail.
	coef, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return Decimal{}, fmt.Errorf("%s isn't a decimal number: %w", quoteShort(s), err)
	}
	if negative {
		coef = -coef
	}
	return Decimal{coef: coef, scale: uint8(scale)}, nil //nolint:gosec // G115: scale is between 0 and maxDecimalDigits here.
}

func allDigits(s string) bool {
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// quoteShort quotes s for an error message, cut short, since it can come
// from a file a source holds.
func quoteShort(s string) string {
	const limit = 40
	if len(s) > limit {
		return strconv.Quote(strings.ToValidUTF8(s[:limit], "")) + "…"
	}
	return strconv.Quote(s)
}

func (Decimal) Kind() Kind { return KindDecimal }
func (Decimal) value()     {}

// String writes d in its shortest form, such as 1, -0.5 or 12.3.
func (d Decimal) String() string {
	s := strconv.FormatInt(d.coef, 10)
	sign, digits := "", s
	if s[0] == '-' {
		sign, digits = "-", s[1:]
	}
	if d.scale == 0 {
		return s
	}
	scale := int(d.scale)
	if len(digits) <= scale {
		digits = strings.Repeat("0", scale-len(digits)+1) + digits
	}
	return sign + digits[:len(digits)-scale] + "." + digits[len(digits)-scale:]
}

// Places is how many digits d has after the decimal point.
func (d Decimal) Places() int { return int(d.scale) }

// Digits is how many digits d has in all, counted as Django REST
// Framework's DecimalField counts them against max_digits: the digits before
// the decimal point, none for a number below 1, and those after it.
func (d Decimal) Digits() int {
	coef := strings.TrimPrefix(strconv.FormatInt(d.coef, 10), "-")
	whole := max(len(coef)-int(d.scale), 0)
	if d.coef == 0 {
		whole = 1
	}
	return whole + int(d.scale)
}

// Cmp compares d and e as numbers: -1 if d < e, 0 if they are equal, and +1
// if d > e.
func (d Decimal) Cmp(e Decimal) int {
	a, b := scaled(d, e)
	return a.Cmp(b)
}

// MultipleOf reports whether d is a whole multiple of step, as NetBox
// requires a device type's height to be of 0.5. A zero step has no
// multiples.
func (d Decimal) MultipleOf(step Decimal) bool {
	if step.coef == 0 {
		return false
	}
	a, b := scaled(d, step)
	return new(big.Int).Rem(a, b).Sign() == 0
}

// scaled returns the coefficients of d and e at the larger of their scales,
// as big integers, which can't overflow.
func scaled(d, e Decimal) (a, b *big.Int) {
	a, b = big.NewInt(d.coef), big.NewInt(e.coef)
	switch {
	case d.scale < e.scale:
		a.Mul(a, pow10(e.scale-d.scale))
	case e.scale < d.scale:
		b.Mul(b, pow10(d.scale-e.scale))
	}
	return a, b
}

func pow10(n uint8) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
}
