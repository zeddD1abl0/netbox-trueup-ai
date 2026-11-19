package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/format"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store"
)

func newListCommand(a *app) *cobra.Command {
	var (
		out    output
		fetch  bool
		search string
	)
	cmd := &cobra.Command{
		Use:   "list [source...]",
		Short: "List the definitions in the configured sources",
		Long: `List the definitions in every configured source, or in those named: the
objects each one defines, read from the version of the source that was last
fetched. For devicetype-library, that is its device types, and a manufacturer
for each name they give, with the slug NetBox's web UI would make from the
name, which TrueUp uses only to create the manufacturer. --search lists only
those whose manufacturer, model or part number holds the text given,
ignoring case.

The definitions are read from TrueUp's database, where "source fetch" loads
each version it fetches. A version the database holds nothing of, or what
another release of TrueUp or other settings of the source read, is loaded
first, which takes a few seconds for devicetype-library, after waiting for
another process's fetch of the source, if one is running. A version whose
load failed reports what it found, until a fetch reads it again.

Files that fail to read are reported with their path, line and reason, and
skipped: YAML that isn't valid, or that uses what TrueUp doesn't read, such
as aliases or tags; a file that fails the library's own JSON schemas, from
the same version of the source; and values NetBox would refuse. Warnings
point at what TrueUp reads but NetBox may not take as it is, such as two
manufacturers that would get the same slug.

The source isn't fetched unless --fetch is given: then each source is
fetched first, as "source fetch" fetches it. A source whose fetch fails
isn't listed, even if an earlier version is in the cache: list without
--fetch lists that.

Exit codes:
  0  Every source was read, and every file in it is valid.
  1  The command couldn't run, such as when the data directory can't be
     written to, or the database can't be opened, read or written.
  2  A name given isn't a configured source, or --search holds a control
     character, such as a line break.
  3  The configuration is invalid, no sources are configured, or
     database.url names a database this release can't use.
  4  Some sources couldn't be read, such as one never fetched, or whose
     cache can't be read or, with --fetch, whose fetch failed, or some
     files are invalid. The result is printed as usual, with each
     problem.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.loadConfig(cmd)
			if err != nil {
				return err
			}
			sources, err := chooseSources(cfg.Settings.Sources, args)
			if err != nil {
				return err
			}
			searching := cmd.Flags().Changed("search")
			if searching {
				if search = strings.TrimSpace(search); search == "" {
					return usageError{errors.New("--search needs some text to look for")}
				}
				if _, err := store.PrepareSearch(search); err != nil {
					return usageError{fmt.Errorf("--search: %w", err)}
				}
			}
			result, err := a.runList(cmd.Context(), cfg, sources, fetch, search)
			if err != nil {
				return err
			}
			if searching {
				result.Search = &search
			}
			if out == outputJSON {
				err = writeJSON(cmd.OutOrStdout(), result)
			} else {
				err = writeListText(cmd.OutOrStdout(), result)
			}
			if err != nil {
				return err
			}
			if !result.OK {
				return outcomeError{code: exitListProblems, err: errors.New(result.summary())}
			}
			return nil
		},
	}
	addOutputFlag(cmd, &out)
	cmd.Flags().BoolVar(&fetch, "fetch", false, "fetch each source first, as source fetch does")
	cmd.Flags().StringVar(&search, "search", "", "list only the definitions whose manufacturer, model or part number holds this text, ignoring case")
	return cmd
}

// runList opens the database, fetches the sources if asked, and reads
// their definitions. It closes the database before it returns, so that
// nothing is printed if closing it fails.
func (a *app) runList(ctx context.Context, cfg *config.Config, sources []config.Source, fetch bool, search string) (_ listJSON, err error) {
	db, _, err := a.openStore(ctx, cfg)
	if err != nil {
		return listJSON{}, err
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	fetchErrors := map[string]string{}
	if fetch {
		root, err := cacheRoot(cfg)
		if err != nil {
			return listJSON{}, err
		}
		fetched, err := a.fetchInto(ctx, db, cfg, root, sources)
		if err != nil {
			return listJSON{}, err
		}
		fetchErrors = fetchProblems(fetched)
	}
	return a.listSources(ctx, db, cfg, sources, fetchErrors, search)
}

// listJSON is the result of `list`, printed as text or JSON. Its JSON fields
// are part of the CLI's public interface (ADR 0018).
type listJSON struct {
	OK bool `json:"ok"` // every source was read, and every file is valid
	// Search is the text --search looks for, or null.
	Search  *string          `json:"search"`
	Sources []listSourceJSON `json:"sources"`
}

type listSourceJSON struct {
	Name   string `json:"name"`
	Format string `json:"format"`
	// Ref and Version are those of the version read, or null when the
	// source couldn't be read.
	Ref     *string `json:"ref"`
	Version *string `json:"version"`
	// OK says the source was read, and every file in it is valid.
	OK bool `json:"ok"`
	// Error says why the source couldn't be read, or is null.
	Error *string `json:"error"`
	// Files is how many files were read as definitions, and InvalidFiles
	// how many of them were skipped with an error.
	Files        int `json:"files"`
	InvalidFiles int `json:"invalid_files"`
	// Definitions are the definitions listed, each as
	// format.MarshalDefinition writes it.
	Definitions []json.RawMessage `json:"definitions"`
	Problems    []problemJSON     `json:"problems"`

	// defs are the Definitions, for text output.
	defs []format.Definition
}

type problemJSON struct {
	Severity string `json:"severity"`
	// Path is the file, or null for a problem with what was derived from
	// several files.
	Path *string `json:"path"`
	// Line and Column are where in the file, or null.
	Line   *int `json:"line"`
	Column *int `json:"column"`
	// Field is where in the file's structure, such as interfaces[3].type,
	// or null.
	Field  *string `json:"field"`
	Reason string  `json:"reason"`
}

// summary says what went wrong, for the error after the result.
func (r listJSON) summary() string {
	var failed []string
	invalid, fileless := 0, 0
	for _, s := range r.Sources {
		if s.Error != nil {
			failed = append(failed, s.Name)
		}
		invalid += s.InvalidFiles
		fileless += filelessErrors(s.Problems)
	}
	var parts []string
	if len(failed) > 0 {
		parts = append(parts, fmt.Sprintf("%d of the %d sources couldn't be read: %s", len(failed), len(r.Sources), strings.Join(failed, ", ")))
	}
	return strings.Join(append(parts, problemSummary(invalid, fileless)...), "; ")
}

// filelessErrors counts the errors that concern no one file, such as a
// derived definition TrueUp couldn't keep, which make a source not OK
// (readCleanly) without any file being invalid.
func filelessErrors(ps []problemJSON) int {
	n := 0
	for _, p := range ps {
		if p.Severity == string(format.Error) && p.Path == nil {
			n++
		}
	}
	return n
}

// problemSummary says how many files are invalid, and how many errors
// concern no one file, for the error after a result.
func problemSummary(invalid, fileless int) []string {
	var parts []string
	switch {
	case invalid == 1:
		parts = append(parts, "1 file is invalid")
	case invalid > 1:
		parts = append(parts, fmt.Sprintf("%d files are invalid", invalid))
	}
	switch {
	case fileless == 1:
		parts = append(parts, "1 error concerns no one file")
	case fileless > 1:
		parts = append(parts, fmt.Sprintf("%d errors concern no one file", fileless))
	}
	return parts
}

// listSources reads the definitions of each source's last fetched version
// from the database, loading the version first if need be, those search
// chooses if it isn't "". A source that can't be read is reported in the
// result, with fetchErrors giving why for those --fetch couldn't fetch or
// load (fetchProblems); a problem with the database, or an interrupt, is
// an error.
func (a *app) listSources(ctx context.Context, db store.Store, cfg *config.Config, sources []config.Source,
	fetchErrors map[string]string, search string,
) (listJSON, error) {
	result := listJSON{OK: true, Sources: []listSourceJSON{}}
	for _, s := range sources {
		row := listSourceJSON{Name: s.Name, Format: s.Format, Definitions: []json.RawMessage{}, Problems: []problemJSON{}}
		fail := func(msg string) {
			row.OK, row.Error = false, &msg
			a.logger.Warn("can't read source", "source", s.Name, "error", msg)
		}
		if msg, ok := fetchErrors[s.Name]; ok {
			fail(msg)
		} else if read, err := a.readLoaded(ctx, db, cfg, s, search); err != nil {
			if ctx.Err() != nil {
				return listJSON{}, fmt.Errorf("interrupted while reading source %s: %w", s.Name, ctx.Err())
			}
			if _, ok := errors.AsType[sourceProblem](err); !ok {
				return listJSON{}, err
			}
			fail(err.Error())
		} else {
			res := read.result
			row.Ref, row.Version = &read.ref, &read.version
			row.Files, row.InvalidFiles = res.Files, res.Invalid
			row.OK = readCleanly(res)
			row.Definitions, row.defs = make([]json.RawMessage, len(read.rows)), res.Definitions
			for i, d := range read.rows {
				row.Definitions[i] = d.JSON
			}
			row.Problems = problemsOf(res.Problems)
		}
		result.OK = result.OK && row.OK
		result.Sources = append(result.Sources, row)
	}
	return result, nil
}

// fetchProblems returns why each source that --fetch fetched can't be read,
// by name: its fetch failed, or the load that followed couldn't read its
// files, which reading them again would find too.
func fetchProblems(fetched fetchJSON) map[string]string {
	problems := map[string]string{}
	for _, s := range fetched.Sources {
		switch {
		case s.Error != nil:
			problems[s.Name] = "the fetch failed: " + *s.Error
		case s.LoadError != nil:
			problems[s.Name] = *s.LoadError
		}
	}
	return problems
}

// readCleanly reports whether a source's files all read as definitions:
// none invalid, and no error that concerns no one file, such as a derived
// definition TrueUp couldn't keep.
func readCleanly(res *format.Result) bool {
	return res.Invalid == 0 && !slices.ContainsFunc(res.Problems, func(p format.Problem) bool { return p.Severity == format.Error })
}

// problemsOf returns problems as list and compare print them.
func problemsOf(ps []format.Problem) []problemJSON {
	out := make([]problemJSON, 0, len(ps))
	for _, p := range ps {
		out = append(out, problemOf(p))
	}
	return out
}

func problemOf(p format.Problem) problemJSON {
	out := problemJSON{Severity: string(p.Severity), Reason: p.Reason}
	if p.Path != "" {
		out.Path = &p.Path
	}
	if p.Line > 0 {
		out.Line = &p.Line
	}
	if p.Column > 0 {
		out.Column = &p.Column
	}
	if p.Field != "" {
		out.Field = &p.Field
	}
	return out
}

// typeNames name object types for text output.
var typeNames = map[string]string{
	"dcim.manufacturer": "manufacturer",
	"dcim.devicetype":   "device type",
}

func writeListText(w io.Writer, r listJSON) error {
	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "SOURCE\tREF\tVERSION\tFILES\tINVALID\tWARNINGS\tMANUFACTURERS\tDEVICE TYPES")
	for _, s := range r.Sources {
		if s.Error != nil {
			_, _ = fmt.Fprintf(tw, "%s\t-\t-\t-\t-\t-\t-\t-\n", s.Name)
			continue
		}
		count := map[string]int{}
		for _, d := range s.defs {
			count[d.Object.Type]++
		}
		warnings := 0
		for _, p := range s.Problems {
			if p.Severity == string(format.Warning) {
				warnings++
			}
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%d\t%d\t%d\n", s.Name, cell(*s.Ref), cell(*s.Version),
			s.Files, s.InvalidFiles, warnings, count["dcim.manufacturer"], count["dcim.devicetype"])
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("write output: %w", err)
	}

	if slices.ContainsFunc(r.Sources, func(s listSourceJSON) bool { return len(s.Definitions) > 0 }) {
		b.WriteString("\n")
		tw = tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "SOURCE\tTYPE\tMANUFACTURER\tMODEL\tSLUG\tFROM")
		for _, s := range r.Sources {
			for _, line := range definitionLines(s) {
				_, _ = fmt.Fprintln(tw, line)
			}
		}
		if err := tw.Flush(); err != nil {
			return fmt.Errorf("write output: %w", err)
		}
	}

	for _, section := range []struct {
		title    string
		severity format.Severity
	}{{"Warnings", format.Warning}, {"Invalid files", format.Error}} {
		var lines []string
		for _, s := range r.Sources {
			for _, p := range s.Problems {
				if p.Severity == string(section.severity) {
					lines = append(lines, "  "+s.Name+": "+cell(problemText(p)))
				}
			}
		}
		if len(lines) > 0 {
			b.WriteString("\n" + section.title + ":\n" + strings.Join(lines, "\n") + "\n")
		}
	}
	var errs []string
	for _, s := range r.Sources {
		if s.Error != nil {
			errs = append(errs, "  "+s.Name+": "+cell(*s.Error))
		}
	}
	if len(errs) > 0 {
		b.WriteString("\nErrors:\n" + strings.Join(errs, "\n") + "\n")
	}
	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

// definitionLines returns a source's definitions as rows of the text
// output's table: each manufacturer, followed by its device types.
func definitionLines(s listSourceJSON) []string {
	byManufacturer := map[string][]format.Definition{}
	var manufacturers []format.Definition
	for _, d := range s.defs {
		switch d.Object.Type {
		case "dcim.manufacturer":
			manufacturers = append(manufacturers, d)
		default:
			name := text(d.Identity["manufacturer"])
			byManufacturer[name] = append(byManufacturer[name], d)
		}
	}
	var lines []string
	row := func(d format.Definition, manufacturer, model string) {
		slug := text(d.Object.Fields["slug"])
		if slices.Contains(d.Derived, "slug") {
			slug += " (derived)"
		}
		from := "-"
		switch {
		case d.Path != "":
			from = d.Path
		case len(d.DerivedFrom) == 1:
			from = "1 file"
		case len(d.DerivedFrom) > 1:
			from = fmt.Sprintf("%d files", len(d.DerivedFrom))
		}
		typ := typeNames[d.Object.Type]
		if typ == "" {
			typ = d.Object.Type
		}
		lines = append(lines, strings.Join([]string{s.Name, typ, cell(manufacturer), cell(model), cell(slug), cell(from)}, "\t"))
	}
	for _, m := range manufacturers {
		name := text(m.Identity["name"])
		row(m, name, "-")
		for _, d := range byManufacturer[name] {
			row(d, name, text(d.Identity["model"]))
		}
		delete(byManufacturer, name)
	}
	// Definitions whose manufacturer isn't listed are still listed, after
	// the others: those --search finds without their manufacturer, or a
	// format that names a manufacturer it doesn't define.
	for _, name := range slices.Sorted(maps.Keys(byManufacturer)) {
		for _, d := range byManufacturer[name] {
			row(d, name, text(d.Identity["model"]))
		}
	}
	return lines
}

// text returns a value as plain text for a table: a string as it is, a
// reference by its name, and anything else as the value writes itself.
func text(v model.Value) string {
	switch v := v.(type) {
	case nil:
		return "-"
	case model.String:
		return string(v)
	case model.Reference:
		return text(v.Key["name"])
	}
	return v.String()
}

// problemText writes a problem as path:line:column: field: reason.
func problemText(p problemJSON) string {
	fp := format.Problem{Severity: format.Severity(p.Severity), Reason: p.Reason}
	if p.Path != nil {
		fp.Path = *p.Path
	}
	if p.Line != nil {
		fp.Line = *p.Line
	}
	if p.Column != nil {
		fp.Column = *p.Column
	}
	if p.Field != nil {
		fp.Field = *p.Field
	}
	return fp.String()
}
