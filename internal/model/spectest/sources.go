package spectest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
)

// ErrNoReference is returned, wrapped, by ReferenceSource when `make
// reference` hasn't fetched any of NetBox's source.
var ErrNoReference = errors.New("NetBox's source isn't in .cache/reference")

// ReferenceSource returns the netbox directory of NetBox's source at release,
// such as 4.7.2, as `make reference` fetches it into .cache/reference at the
// top of the repository. It returns ErrNoReference, wrapped, when nothing has
// been fetched, and another error when the source of other releases has.
func ReferenceSource(release string) (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("the repository's go.mod isn't above this directory")
		}
		dir = parent
	}
	cache := filepath.Join(dir, ".cache", "reference")
	root := filepath.Join(cache, "netbox-"+release, "netbox")
	if info, err := os.Stat(root); err == nil && info.IsDir() { //nolint:gosec // G703: the repository's own .cache, found from the working directory.
		return root, nil
	}
	others, _ := filepath.Glob(filepath.Join(cache, "netbox-*"))
	if len(others) == 0 {
		return "", fmt.Errorf("%w: run make reference", ErrNoReference)
	}
	return "", fmt.Errorf("%s holds the source of %s, but not of NetBox %s, which the snapshot is from: run make reference",
		cache, strings.Join(others, ", "), release)
}

// Sources reports every citation in specs that NetBox's source doesn't bear
// out: a file that isn't under root, NetBox's netbox directory at the target
// release, or a symbol, such as DeviceType or DeviceType.clean, that it
// doesn't define, each part of a dotted symbol within the class before it. It
// also checks each choice field's Choices.Configurable against the choice
// set the field cites, which an administrator can change through
// FIELD_CHOICES when it has a key. It returns nil when it finds nothing.
func Sources(root string, specs []*model.Spec) error {
	dir, err := os.OpenRoot(root)
	if err != nil {
		return fmt.Errorf("NetBox's source: %w", err)
	}
	defer func() { _ = dir.Close() }()
	r := reader{dir: dir, files: map[string][]string{}, failed: map[string]error{}}
	var problems []error
	for _, s := range specs {
		for _, src := range citations(s) {
			if _, err := r.definition(src); err != nil {
				problems = append(problems, fmt.Errorf("%s cites %s in %s: %w", s.Type, src.Symbol, src.File, err))
			}
		}
		for _, f := range choiceFields(s) {
			if err := r.checkConfigurable(f.field); err != nil {
				problems = append(problems, fmt.Errorf("%s.%s: %w", s.Type, f.name, err))
			}
		}
	}
	return errors.Join(problems...)
}

// reader reads NetBox's source files, once each.
type reader struct {
	dir    *os.Root
	files  map[string][]string // file -> lines
	failed map[string]error
}

func (r *reader) lines(file string) ([]string, error) {
	if err, ok := r.failed[file]; ok {
		return nil, err
	}
	if lines, ok := r.files[file]; ok {
		return lines, nil
	}
	data, err := r.dir.ReadFile(file)
	if err != nil {
		r.failed[file] = err
		return nil, err
	}
	lines := strings.Split(string(data), "\n")
	r.files[file] = lines
	return lines, nil
}

// definition returns the lines that define src's symbol: its first line and
// the lines indented under it. Each part of a dotted symbol is looked for
// among the lines that define the part before it.
func (r *reader) definition(src model.Source) ([]string, error) {
	lines, err := r.lines(src.File)
	if err != nil {
		return nil, err
	}
	level := 0 // the indentation of a module's or block's own statements
	for part := range strings.SplitSeq(src.Symbol, ".") {
		if lines = block(lines, part, level); lines == nil {
			return nil, fmt.Errorf("%s doesn't define %s where the citation says", src.File, part)
		}
		level = bodyIndent(lines)
	}
	return lines, nil
}

// code reports, for each line, whether it is a line of code: not blank, not
// a comment, and not inside a string over several lines, such as a docstring,
// which can have lines at any indentation.
func code(lines []string) []bool {
	out := make([]bool, len(lines))
	inString := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		starting := inString
		if strings.Count(line, `"""`)%2 == 1 || strings.Count(line, `'''`)%2 == 1 {
			inString = !inString
		}
		out[i] = trimmed != "" && !starting
	}
	return out
}

func indentation(line string) int {
	return len(line) - len(strings.TrimLeft(line, " \t"))
}

// bodyIndent is the indentation of the first line of code after a block's
// first line: that of the statements in its body.
func bodyIndent(block []string) int {
	isCode := code(block)
	for i := 1; i < len(block); i++ {
		if isCode[i] {
			return indentation(block[i])
		}
	}
	return -1
}

// block finds the shallowest definition of name among lines, as a class or
// function, or by an assignment among the statements at indentation level,
// such as a module's constant or a class's attribute, and returns it with the
// lines indented under it, or nil if there is none. An assignment further
// in, such as a keyword argument, doesn't count, and nor does anything in a
// comment or string.
func block(lines []string, name string, level int) []string {
	defined := regexp.MustCompile(`^(\s*)(?:class|def)\s+` + regexp.QuoteMeta(name) + `\b`)
	assigned := regexp.MustCompile(`^(\s*)` + regexp.QuoteMeta(name) + `\s*(:[^=]*)?=([^=]|$)`)
	isCode := code(lines)
	start, indent := -1, 0
	for i, line := range lines {
		if !isCode[i] {
			continue
		}
		m := defined.FindStringSubmatch(line)
		if m == nil {
			if m = assigned.FindStringSubmatch(line); m != nil && len(m[1]) != level {
				m = nil
			}
		}
		if m != nil && (start < 0 || len(m[1]) < indent) {
			start, indent = i, len(m[1])
		}
	}
	if start < 0 {
		return nil
	}
	// The block ends at the first line of code indented no deeper than its
	// first.
	end := start + 1
	for end < len(lines) && (!isCode[end] || indentation(lines[end]) > indent) {
		end++
	}
	return lines[start:end]
}

// choiceKey matches a ChoiceSet's key, the name under which FIELD_CHOICES
// can change its choices (ChoiceSetMeta in utilities/choices.py).
var choiceKey = regexp.MustCompile(`^\s*key\s*=\s*['"]([^'"]+)['"]`)

// checkConfigurable checks that a choice field's Choices.Configurable is the
// key NetBox builds for the choice set it cites: the first part of the
// set's module, such as dcim, and the set's key, such as Device.airflow. A
// set without a key can't be changed.
func (r *reader) checkConfigurable(f *model.Field) error {
	def, err := r.definition(f.Source)
	if err != nil {
		return err
	}
	if !strings.Contains(strings.Join(def, "\n"), "CHOICES") {
		return fmt.Errorf("it cites %s, which isn't a choice set", f.Source.Symbol)
	}
	want := ""
	if key := block(def, "key", bodyIndent(def)); key != nil {
		m := choiceKey.FindStringSubmatch(key[0])
		if m == nil {
			return fmt.Errorf("%s's key isn't a string NetBox's source spells out", f.Source.Symbol)
		}
		app, _, _ := strings.Cut(f.Source.File, "/")
		want = app + "." + m[1]
	}
	if f.Choices.Configurable != want {
		return fmt.Errorf("its FIELD_CHOICES key is %q, but %s's is %q", f.Choices.Configurable, f.Source.Symbol, want)
	}
	return nil
}

type namedField struct {
	name  string
	field *model.Field
}

// choiceFields returns a spec's choice fields that cite their choice set,
// set elements included, under the field's name.
func choiceFields(s *model.Spec) []namedField {
	var out []namedField
	for i := range s.Fields {
		f := &s.Fields[i]
		name := f.Name
		if f.Elem != nil {
			f, name = f.Elem, name+"[]"
		}
		if f.Kind == model.KindChoice && f.Choices != nil && f.Source != (model.Source{}) {
			out = append(out, namedField{name, f})
		}
	}
	return out
}

// citations returns every source a spec cites.
func citations(s *model.Spec) []model.Source {
	srcs := append([]model.Source(nil), s.Sources...)
	add := func(src model.Source) {
		if src != (model.Source{}) {
			srcs = append(srcs, src)
		}
	}
	for _, u := range s.Unique {
		add(u.Source)
	}
	for _, f := range s.Fields {
		add(f.Source)
		add(f.TighterBounds)
		if f.Elem != nil {
			add(f.Elem.Source)
			add(f.Elem.TighterBounds)
		}
	}
	for _, col := range s.Children {
		add(col.Source)
	}
	return srcs
}
