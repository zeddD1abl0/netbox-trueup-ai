package model

import (
	"cmp"
	"fmt"
	"time"
)

// Date is a calendar date, with no time of day or time zone, as NetBox's
// DateFields hold, such as a device type's end_of_life. The zero value isn't
// a date; ParseDate makes them.
type Date struct {
	year  int
	month time.Month
	day   int
}

// ParseDate reads a date written as NetBox's API writes one, YYYY-MM-DD,
// such as 2027-03-31. Like Python's dates, the year is 1 to 9999.
func ParseDate(s string) (Date, error) {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil || t.Format(time.DateOnly) != s || t.Year() < 1 {
		return Date{}, fmt.Errorf("%s isn't a date written as YYYY-MM-DD", quoteShort(s))
	}
	return Date{year: t.Year(), month: t.Month(), day: t.Day()}, nil
}

func (Date) Kind() Kind { return KindDate }
func (Date) value()     {}

// String writes d as YYYY-MM-DD.
func (d Date) String() string {
	return fmt.Sprintf("%04d-%02d-%02d", d.year, int(d.month), d.day)
}

// Cmp compares d and e: -1 if d is earlier, 0 if they are the same day, and
// +1 if d is later.
func (d Date) Cmp(e Date) int {
	switch {
	case d.year != e.year:
		return cmp.Compare(d.year, e.year)
	case d.month != e.month:
		return cmp.Compare(d.month, e.month)
	default:
		return cmp.Compare(d.day, e.day)
	}
}
