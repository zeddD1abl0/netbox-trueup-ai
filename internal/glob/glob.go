// Package glob matches slash-separated paths, such as the paths of files in a
// source, against patterns such as device-types/Juniper/**.
//
// A pattern is a list of segments separated by slashes, matched against the
// segments of a path:
//
//   - Within a segment, * matches any run of characters, ? any one
//     character, and [...] one character of a class, or [^...] one
//     character outside it. A backslash makes the character after it match
//     itself. These are path.Match's rules.
//   - ** as a whole segment matches any number of segments, none included.
//   - A pattern that matches a directory matches everything below it, so
//     device-types/Juniper matches what device-types/Juniper/** does.
//
// Matching is case-sensitive.
package glob

import (
	"fmt"
	"path"
	"slices"
	"strings"
)

// anySegments is the segment that matches any number of segments.
const anySegments = "**"

// Pattern is a compiled pattern. The zero Pattern matches nothing.
type Pattern struct {
	text     string
	segments []string
}

// Compile checks a pattern and compiles it. Its error says what is wrong in
// a form that can follow the pattern's name, such as "must not be empty".
func Compile(text string) (Pattern, error) {
	if problem := Problem(text); problem != "" {
		return Pattern{}, fmt.Errorf("pattern %q %s", text, problem)
	}
	return Pattern{text: text, segments: strings.Split(text, "/")}, nil
}

// Problem returns what is wrong with a pattern, or "" if nothing is. The
// configuration checks patterns with it, so that a pattern it accepts always
// compiles.
func Problem(text string) string {
	switch {
	case text == "":
		return "must not be empty"
	case strings.HasPrefix(text, "/"):
		return "must be relative to the top of the source, with no leading /"
	case strings.HasSuffix(text, "/"):
		return "must not end with /: a pattern that matches a directory matches everything in it"
	}
	for seg := range strings.SplitSeq(text, "/") {
		switch {
		case seg == "":
			return "must not contain //"
		case seg == "." || seg == "..":
			return "must not contain . or .. as a segment"
		case seg == anySegments:
		case strings.Contains(seg, anySegments):
			return "must use ** only as a whole segment, such as device-types/**/*.yaml"
		default:
			// path.Match checks the whole pattern, whatever the name.
			if _, err := path.Match(seg, ""); err != nil {
				return fmt.Sprintf("has a segment, %q, that isn't a valid pattern: check its [ ] and \\", seg)
			}
		}
	}
	return ""
}

// String returns the pattern as it was written.
func (p Pattern) String() string {
	return p.text
}

// Match reports whether the pattern matches a path, or a directory above it.
// The path is slash-separated, relative and clean.
func (p Pattern) Match(name string) bool {
	matched, _ := p.run(name)
	return matched
}

// MatchBelow reports whether the pattern matches a directory, a directory
// above it, or could match a path below it: whether a walk looking for
// matches needs to look inside the directory.
func (p Pattern) MatchBelow(dir string) bool {
	matched, open := p.run(dir)
	return matched || open
}

// run matches the pattern against the parts of name. matched says whether
// the whole pattern matches name or a directory above it, and open whether
// part of the pattern matches all of name, so that the rest could match a
// path below it.
func (p Pattern) run(name string) (matched, open bool) {
	n := len(p.segments)
	if n == 0 || name == "" {
		return false, false
	}
	// at[i] holds when the first i segments of the pattern match the parts
	// of the path read so far. Tracking every i keeps matching linear in the
	// length of the pattern and the path, however many ** it has.
	at := make([]bool, n+1)
	next := make([]bool, n+1)
	at[0] = true
	p.skipAny(at)
	for part := range strings.SplitSeq(name, "/") {
		clear(next)
		for i, seg := range p.segments {
			switch {
			case !at[i]:
			case seg == anySegments:
				next[i] = true // ** takes this part too
			case matchSegment(seg, part):
				next[i+1] = true
			}
		}
		p.skipAny(next)
		at, next = next, at
		if at[n] {
			// The whole pattern matches the path so far: the path itself,
			// or a directory above it.
			return true, true
		}
	}
	return false, slices.Contains(at, true)
}

// skipAny lets each ** that the path has reached match no segments.
func (p Pattern) skipAny(at []bool) {
	for i, seg := range p.segments {
		if at[i] && seg == anySegments {
			at[i+1] = true
		}
	}
}

func matchSegment(pattern, part string) bool {
	ok, err := path.Match(pattern, part)
	return err == nil && ok // Compile has checked the pattern
}

// MatchAny reports whether any of the patterns matches the path.
func MatchAny(patterns []Pattern, name string) bool {
	return slices.ContainsFunc(patterns, func(p Pattern) bool { return p.Match(name) })
}
