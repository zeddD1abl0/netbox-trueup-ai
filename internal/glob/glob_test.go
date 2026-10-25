package glob

import (
	"path"
	"strings"
	"testing"
)

func TestProblem(t *testing.T) {
	t.Parallel()

	tests := []struct {
		pattern string
		want    string // a substring of the problem; empty for none
	}{
		{"device-types/Juniper/**", ""},
		{"device-types/Juniper", ""},
		{"**", ""},
		{"**/*.yaml", ""},
		{"a/**/b/**", ""},
		{"device-types/[A-C]*/*.y?ml", ""},
		{`a\*b`, ""},
		{"", "must not be empty"},
		{"/device-types", "no leading /"},
		{"device-types/", "must not end with /"},
		{"a//b", "must not contain //"},
		{"a/../b", "must not contain . or .."},
		{"./a", "must not contain . or .."},
		{"a/**b", "** only as a whole segment"},
		{"***", "** only as a whole segment"},
		{"a/[b", `segment, "[b", that isn't a valid pattern`},
		{`a/b\`, "isn't a valid pattern"},
	}
	for _, tt := range tests {
		got := Problem(tt.pattern)
		if tt.want == "" && got != "" || !strings.Contains(got, tt.want) {
			t.Errorf("Problem(%q) = %q, want %q", tt.pattern, got, tt.want)
		}
		if _, err := Compile(tt.pattern); (err != nil) != (tt.want != "") {
			t.Errorf("Compile(%q) error = %v, want an error: %v", tt.pattern, err, tt.want != "")
		}
	}
}

func TestMatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		pattern string
		path    string
		want    bool
	}{
		// A directory, with or without **, covers everything below it.
		{"device-types/Juniper/**", "device-types/Juniper/MX204.yaml", true},
		{"device-types/Juniper/**", "device-types/Juniper/sub/MX204.yaml", true},
		{"device-types/Juniper", "device-types/Juniper/MX204.yaml", true},
		{"device-types/Juniper", "device-types/Juniper", true},
		{"device-types/Juniper/**", "device-types/Juniper", true},
		{"device-types/Juniper", "device-types/Juniper-old/MX204.yaml", false},
		{"device-types/Juniper/**", "device-types/Cisco/x.yaml", false},
		{"device-types/Juniper", "device-types", false},
		// Matching is case-sensitive.
		{"device-types/juniper", "device-types/Juniper/MX204.yaml", false},
		// * and ? stay within a segment.
		{"device-types/*.yaml", "device-types/x.yaml", true},
		{"device-types/*.yaml", "device-types/Juniper/x.yaml", false},
		{"device-types/*", "device-types/Juniper/x.yaml", true}, // matches the directory
		{"*.yaml", "x.yaml", true},
		{"*.yaml", "device-types/x.yaml", false},
		{"x.y?ml", "x.yaml", true},
		{"x.y?ml", "x.yml", false},
		// ** matches any number of segments, none included.
		{"**/*.yaml", "x.yaml", true},
		{"**/*.yaml", "a/b/c/x.yaml", true},
		{"**/*.yaml", "a/b/c/x.json", false},
		{"device-types/**/*.yaml", "device-types/x.yaml", true},
		{"device-types/**/*.yaml", "device-types/a/b/x.yaml", true},
		{"device-types/**/*.yaml", "module-types/a/x.yaml", false},
		{"**", "anything/at/all", true},
		{"a/**/b/**/c", "a/b/c", true},
		{"a/**/b/**/c", "a/x/b/y/z/c/d", true},
		{"a/**/b/**/c", "a/x/y/c", false},
		// Classes and escapes follow path.Match.
		{"device-types/[A-C]*", "device-types/Cisco/x.yaml", true},
		{"device-types/[A-C]*", "device-types/Juniper/x.yaml", false},
		{"device-types/[^A-C]*", "device-types/Juniper/x.yaml", true},
		{`a\*`, "a*", true},
		{`a\*`, "ab", false},
		// The zero Pattern and an empty path match nothing.
		{"**", "", false},
	}
	for _, tt := range tests {
		p, err := Compile(tt.pattern)
		if err != nil {
			t.Fatalf("Compile(%q): %v", tt.pattern, err)
		}
		if got := p.Match(tt.path); got != tt.want {
			t.Errorf("Compile(%q).Match(%q) = %v, want %v", tt.pattern, tt.path, got, tt.want)
		}
	}
	if (Pattern{}).Match("a") {
		t.Error("the zero Pattern matched a path")
	}
}

func TestMatchBelow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		pattern string
		dir     string
		want    bool
	}{
		{"device-types/Juniper/**", "device-types", true},
		{"device-types/Juniper/**", "device-types/Juniper", true},
		{"device-types/Juniper/**", "device-types/Juniper/sub", true},
		{"device-types/Juniper/**", "elevation-images", false},
		{"device-types/Juniper/**", "device-types/Cisco", false},
		{"device-types/*/*.yaml", "device-types/Cisco", true},
		{"device-types/*/*.yaml", "device-types/Cisco/sub", false},
		{"**/*.yaml", "anything/below", true},
		{"*.yaml", "dir", false},
		{"schema", "schema", true},
	}
	for _, tt := range tests {
		p, err := Compile(tt.pattern)
		if err != nil {
			t.Fatalf("Compile(%q): %v", tt.pattern, err)
		}
		if got := p.MatchBelow(tt.dir); got != tt.want {
			t.Errorf("Compile(%q).MatchBelow(%q) = %v, want %v", tt.pattern, tt.dir, got, tt.want)
		}
	}
}

func TestMatchAny(t *testing.T) {
	t.Parallel()

	var patterns []Pattern
	for _, s := range []string{"device-types/Juniper", "schema/*.json"} {
		p, err := Compile(s)
		if err != nil {
			t.Fatal(err)
		}
		patterns = append(patterns, p)
	}
	for path, want := range map[string]bool{
		"device-types/Juniper/x.yaml": true,
		"schema/devicetype.json":      true,
		"schema/sub/devicetype.json":  false,
		"device-types/Cisco/x.yaml":   false,
	} {
		if got := MatchAny(patterns, path); got != want {
			t.Errorf("MatchAny(%q) = %v, want %v", path, got, want)
		}
	}
	if MatchAny(nil, "x") {
		t.Error("MatchAny with no patterns matched")
	}
}

// FuzzMatch checks Match against a plain recursive matcher, which is easy to
// get right but slow on patterns with many **.
func FuzzMatch(f *testing.F) {
	for _, seed := range [][2]string{
		{"device-types/**/*.yaml", "device-types/a/b.yaml"},
		{"a/**/b/**/c", "a/x/b/y/c/z"},
		{"**", "x"},
		{"a/[b-d]?/**", "a/c1"},
	} {
		f.Add(seed[0], seed[1])
	}
	f.Fuzz(func(t *testing.T, pattern, name string) {
		p, err := Compile(pattern)
		if err != nil {
			return
		}
		if name == "" || strings.Contains(name, "//") || strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") {
			return // paths in a snapshot are clean and relative
		}
		parts := strings.Split(name, "/")
		if len(parts) > 12 || strings.Count(pattern, "**") > 6 {
			return // keep the reference matcher fast
		}
		want := false
		for k := 1; k <= len(parts) && !want; k++ {
			want = reference(p.segments, parts[:k])
		}
		if got := p.Match(name); got != want {
			t.Errorf("Compile(%q).Match(%q) = %v, reference says %v", pattern, name, got, want)
		}
		// A walk must enter every directory above a path that matches.
		for k := 1; want && k < len(parts); k++ {
			if dir := strings.Join(parts[:k], "/"); !p.MatchBelow(dir) {
				t.Errorf("Compile(%q) matches %q, but MatchBelow(%q) is false", pattern, name, dir)
			}
		}
	})
}

func reference(segments, parts []string) bool {
	if len(segments) == 0 {
		return len(parts) == 0
	}
	if segments[0] == anySegments {
		for i := 0; i <= len(parts); i++ {
			if reference(segments[1:], parts[i:]) {
				return true
			}
		}
		return false
	}
	if len(parts) == 0 {
		return false
	}
	ok, err := path.Match(segments[0], parts[0])
	return err == nil && ok && reference(segments[1:], parts[1:])
}
