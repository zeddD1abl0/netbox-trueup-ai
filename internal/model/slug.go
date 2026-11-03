package model

import (
	"strings"
	"unicode/utf8"
)

// SlugLength is the most characters NetBox's web UI puts in a slug it makes
// from a name. The UI cuts a slug to its field's maxlength, and to 50 when
// the field has none, as the slug field of every OrganizationalModelForm,
// such as ManufacturerForm, doesn't (SlugField in
// utilities/forms/fields/fields.py sets no max_length).
const SlugLength = 50

// Slugify makes a slug from a name as NetBox 4.7.2's web UI does when a slug
// field is left empty or its button is pressed: slugify in
// project-static/src/buttons/reslug.ts, which NetBox's server never repeats.
// It drops every character but ASCII letters, digits and _, -, . and white
// space; trims white space and dots from both ends; turns each run of -, .
// and white space into one -; lower-cases; and cuts the result to length
// characters. White space is what JavaScript's \s matches, which differs from
// Go's unicode.IsSpace and Python's str.isspace.
//
// The slug can be empty, as it is for a name with no ASCII letter or digit,
// and can start or end with - or _. NetBox's UI makes the same, and NetBox
// then refuses an empty one.
func Slugify(name string, length int) string {
	// Drop the characters slugify's /[^\-.\w\s]/g removes. What is left is
	// ASCII, apart from white space, which the steps below turn into -.
	kept := make([]rune, 0, len(name))
	for _, r := range name {
		if r == '-' || r == '.' || isWordASCII(r) || isJavaScriptSpace(r) {
			kept = append(kept, r)
		}
	}
	// Trim /^[\s.]+|[\s.]+$/g.
	trimmed := func(r rune) bool { return r == '.' || isJavaScriptSpace(r) }
	start, end := 0, len(kept)
	for start < end && trimmed(kept[start]) {
		start++
	}
	for end > start && trimmed(kept[end-1]) {
		end--
	}
	// Replace /[-.\s]+/g with -, then lower-case.
	var b strings.Builder
	inRun := false
	for _, r := range kept[start:end] {
		if r == '-' || r == '.' || isJavaScriptSpace(r) {
			if !inRun {
				b.WriteByte('-')
			}
			inRun = true
			continue
		}
		inRun = false
		if 'A' <= r && r <= 'Z' {
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	// Cut to length. The slug is ASCII by now, so a character is a byte, as
	// it is a UTF-16 code unit in JavaScript's substring.
	slug := b.String()
	if utf8.RuneCountInString(slug) > length {
		slug = slug[:max(length, 0)]
	}
	return slug
}

// isWordASCII reports whether JavaScript's \w matches r: an ASCII letter,
// digit or underscore, without the regular expression's u and i flags.
func isWordASCII(r rune) bool {
	return 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9' || r == '_'
}

// isJavaScriptSpace reports whether JavaScript's \s matches r: the
// WhiteSpace and LineTerminator characters of ECMAScript 2016 and later,
// which are Unicode's Space_Separator category and a few others, but not
// U+0085, U+180E or the separators U+001C to U+001F.
func isJavaScriptSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0x00a0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return 0x2000 <= r && r <= 0x200a
}
