package spectest

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
)

// Sources reports every citation in specs that NetBox's source doesn't
// bear out: a file that isn't under root, NetBox's netbox directory at the
// target release, or a part of a symbol, such as DeviceType or clean in
// DeviceType.clean, that the file doesn't define as a class or function or
// assign. It returns nil when it finds nothing.
func Sources(root string, specs []*model.Spec) error {
	dir, err := os.OpenRoot(root)
	if err != nil {
		return fmt.Errorf("NetBox's source: %w", err)
	}
	defer func() { _ = dir.Close() }()
	files := map[string]string{}
	var problems []error
	for _, s := range specs {
		for _, src := range citations(s) {
			text, ok := files[src.File]
			if !ok {
				data, err := dir.ReadFile(src.File)
				if err != nil {
					problems = append(problems, fmt.Errorf("%s cites %s: %w", s.Type, src.File, err))
					files[src.File] = ""
					continue
				}
				text = string(data)
				files[src.File] = text
			}
			if text == "" {
				continue
			}
			for part := range strings.SplitSeq(src.Symbol, ".") {
				defined := regexp.MustCompile(`(?m)^\s*((class|def)\s+` + regexp.QuoteMeta(part) + `\b|` + regexp.QuoteMeta(part) + `\s*=)`)
				if !defined.MatchString(text) {
					problems = append(problems, fmt.Errorf("%s cites %s in %s, which doesn't define %s", s.Type, src.Symbol, src.File, part))
				}
			}
		}
	}
	return errors.Join(problems...)
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
		if f.Elem != nil {
			add(f.Elem.Source)
		}
	}
	for _, col := range s.Children {
		add(col.Source)
	}
	return srcs
}
