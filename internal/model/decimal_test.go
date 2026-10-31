package model

import (
	"math"
	"math/big"
	"strings"
	"testing"
)

func TestParseDecimal(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in             string
		want           string
		places, digits int
	}{
		{"0", "0", 0, 1},
		{"-0", "0", 0, 1},
		{"0.0", "0", 0, 1},
		{"1", "1", 0, 1},
		{"1.0", "1", 0, 1},
		{"+1.50", "1.5", 1, 2},
		{"12.35", "12.35", 2, 4},
		{"-12.30", "-12.3", 1, 3},
		{".5", "0.5", 1, 1},
		{"5.", "5", 0, 1},
		{"0.001234", "0.001234", 6, 6},
		{"1234500", "1234500", 0, 7},
		{"00042.00", "42", 0, 2},
		// Python writes floats with exponents, outside 1e-4 to 1e16.
		{"1e-06", "0.000001", 6, 6},
		{"5E-5", "0.00005", 5, 5},
		{"1.5e+16", "15000000000000000", 0, 17},
		{"12.5e1", "125", 0, 3},
		{"1000e-3", "1", 0, 1},
		{"999999999999999999", "999999999999999999", 0, 18},
		{"0.000000000000000001", "0.000000000000000001", 18, 18},
		{"1.000000000000000000000000", "1", 0, 1},
	}
	for _, tt := range tests {
		d, err := ParseDecimal(tt.in)
		if err != nil {
			t.Errorf("ParseDecimal(%q): %v", tt.in, err)
			continue
		}
		if d.String() != tt.want || d.Places() != tt.places || d.Digits() != tt.digits {
			t.Errorf("ParseDecimal(%q) = %s with %d places and %d digits, want %s, %d and %d",
				tt.in, d, d.Places(), d.Digits(), tt.want, tt.places, tt.digits)
		}
	}
}

func TestParseDecimalErrors(t *testing.T) {
	t.Parallel()
	for _, in := range []string{
		"", "-", ".", "+.", "1.2.3", "1,5", "0x10", "1e", "1e+", "e5", "1e5.5",
		"NaN", "Infinity", "inf", " 1", "1 ", "1_000",
		"1e1001", "1e-1001",
		"1234567890123456789",   // 19 significant digits
		"1e18",                  // 19 digits once written out
		"0.0000000000000000001", // 19 places
		strings.Repeat("9", 1000),
	} {
		if d, err := ParseDecimal(in); err == nil {
			t.Errorf("ParseDecimal(%q) = %s, want an error", in, d)
		}
	}
	_, err := ParseDecimal(strings.Repeat("x", 1000))
	if err == nil || len(err.Error()) > 100 {
		t.Errorf("ParseDecimal of 1000 bytes gave %v, want a short error", err)
	}
}

func TestDecimalLowestTerms(t *testing.T) {
	t.Parallel()
	one, err := ParseDecimal("1.00")
	if err != nil {
		t.Fatal(err)
	}
	if one != NewDecimal(1, 0) || one != NewDecimal(10, 1) || one != NewDecimal(1000, 3) {
		t.Errorf("1.00, 1, 1.0 and 1.000 aren't the same Decimal")
	}
	if NewDecimal(0, 5) != (Decimal{}) {
		t.Errorf("NewDecimal(0, 5) isn't the zero Decimal")
	}
	if got := NewDecimal(15, 1).String(); got != "1.5" {
		t.Errorf("NewDecimal(15, 1) = %s, want 1.5", got)
	}
	if got := NewDecimal(math.MinInt64, 2).String(); got != "-92233720368547758.08" {
		t.Errorf("NewDecimal(MinInt64, 2) = %s", got)
	}
}

func TestDecimalCmp(t *testing.T) {
	t.Parallel()
	tests := []struct {
		a, b string
		want int
	}{
		{"1", "1.0", 0},
		{"0.5", "0.50", 0},
		{"1", "1.5", -1},
		{"-1", "-1.5", 1},
		{"999999999999999999", "0.000000000000000001", 1},
		{"-999999999999999999", "0.000000000000000001", -1},
		{"0.000000000000000001", "0.000000000000000002", -1},
	}
	for _, tt := range tests {
		a, b := mustParse(t, tt.a), mustParse(t, tt.b)
		if got := a.Cmp(b); got != tt.want {
			t.Errorf("%s.Cmp(%s) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
		if got := b.Cmp(a); got != -tt.want {
			t.Errorf("%s.Cmp(%s) = %d, want %d", tt.b, tt.a, got, -tt.want)
		}
	}
}

func TestDecimalMultipleOf(t *testing.T) {
	t.Parallel()
	half := mustParse(t, "0.5")
	for in, want := range map[string]bool{
		"0": true, "0.5": true, "1": true, "42.5": true, "-1.5": true,
		"0.25": false, "1.1": false, "0.000000000000000001": false, "999999999999999999": true,
	} {
		if got := mustParse(t, in).MultipleOf(half); got != want {
			t.Errorf("%s.MultipleOf(0.5) = %t, want %t", in, got, want)
		}
	}
	if NewDecimal(1, 0).MultipleOf(Decimal{}) {
		t.Errorf("1 is a multiple of 0")
	}
}

func TestParseDate(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"2027-03-31", "0001-01-01", "9999-12-31", "2028-02-29"} {
		d, err := ParseDate(in)
		if err != nil {
			t.Errorf("ParseDate(%q): %v", in, err)
		} else if d.String() != in {
			t.Errorf("ParseDate(%q) = %s", in, d)
		}
	}
	for _, in := range []string{
		"", "2027-3-31", "2027-03-1", "27-03-31", "2027-02-30", "2027-13-01", "0000-01-01",
		"2027-03-31T00:00:00Z", " 2027-03-31", "2027/03/31", "+2027-03-31",
	} {
		if d, err := ParseDate(in); err == nil {
			t.Errorf("ParseDate(%q) = %s, want an error", in, d)
		}
	}
	a, b, same := mustDate(t, "2027-03-31"), mustDate(t, "2027-04-01"), mustDate(t, "2027-03-31")
	if a.Cmp(b) != -1 || b.Cmp(a) != 1 || a.Cmp(same) != 0 || a != same {
		t.Errorf("dates compare wrongly")
	}
	if mustDate(t, "2026-12-31").Cmp(a) != -1 {
		t.Errorf("an earlier year compares as later")
	}
}

func mustParse(t *testing.T, s string) Decimal {
	t.Helper()
	d, err := ParseDecimal(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func mustDate(t *testing.T, s string) Date {
	t.Helper()
	d, err := ParseDate(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// FuzzParseDecimal checks that what ParseDecimal reads is the number
// math/big reads from the same text, and that its String reads back the same.
func FuzzParseDecimal(f *testing.F) {
	for _, s := range []string{"0", "1.0", "-12.35", "1e-06", "1.5e+16", ".5", "999999999999999999"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		d, err := ParseDecimal(s)
		if err != nil {
			return
		}
		want, ok := new(big.Rat).SetString(s)
		if !ok {
			t.Fatalf("ParseDecimal(%q) = %s, but math/big can't read it", s, d)
		}
		got, ok := new(big.Rat).SetString(d.String())
		if !ok || got.Cmp(want) != 0 {
			t.Fatalf("ParseDecimal(%q) = %s, want %s", s, d, want.FloatString(20))
		}
		again, err := ParseDecimal(d.String())
		if err != nil || again != d {
			t.Fatalf("ParseDecimal(%q) = %v, %v; want %s", d.String(), again, err, d)
		}
	})
}
