package audit

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// maxAgo is the furthest back a duration reaches, about a hundred years: far
// enough for any audit log, near enough that now minus it never overflows.
const maxAgo = 100 * 365 * 24 * time.Hour

// Last is the latest time a filter takes: RFC 3339, in which the store
// writes times, has four-digit years, and SQLite compares them as text.
var Last = time.Date(9999, 12, 31, 23, 59, 59, 999999000, time.UTC)

// ParseTime reads a time the audit command takes: an RFC 3339 time, such as
// 2026-11-23T09:30:00Z or 2026-11-23T19:30:00+10:00, or a duration back from
// now, as Go writes one, such as 90m or 1h30m, or a whole number of days,
// such as 7d. It returns the time in UTC.
func ParseTime(s string, now time.Time) (time.Time, error) {
	if s == "" {
		return time.Time{}, errors.New("is empty")
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		if t.IsZero() {
			// A Filter takes the zero time for no bound.
			return time.Time{}, fmt.Errorf("%s is the zero time, which stands for no bound: give a later time", strconv.Quote(s))
		}
		if t.After(Last) {
			return time.Time{}, fmt.Errorf("%s is later than the last time the audit log can be searched by, %s", strconv.Quote(s), Last.Format(time.RFC3339Nano))
		}
		return t.UTC(), nil
	}
	ago, err := parseAgo(s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s is neither an RFC 3339 time, such as 2026-11-23T09:30:00Z, nor a duration back from now, such as 90m, 24h or 7d", strconv.Quote(s))
	}
	return now.Add(-ago).UTC(), nil
}

// parseAgo reads a duration that isn't negative and reaches back no further
// than maxAgo: Go's, or a whole number of days.
func parseAgo(s string) (time.Duration, error) {
	var d time.Duration
	if days, ok := strings.CutSuffix(s, "d"); ok {
		// ParseUint refuses a sign.
		n, err := strconv.ParseUint(days, 10, 32)
		if err != nil {
			return 0, errors.New("not a number of days")
		}
		if n > uint64(maxAgo/(24*time.Hour)) {
			return 0, errors.New("too far back")
		}
		d = time.Duration(n) * 24 * time.Hour
	} else {
		var err error
		if d, err = time.ParseDuration(s); err != nil {
			return 0, err
		}
	}
	if d < 0 || d > maxAgo {
		return 0, errors.New("negative, or too far back")
	}
	return d, nil
}
