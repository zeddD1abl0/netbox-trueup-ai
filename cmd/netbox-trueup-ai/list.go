package main

import (
	"context"
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
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/glob"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/source"
)

func newListCommand(a *app) *cobra.Command {
	var (
		out   output
		fetch bool
	)
	cmd := &cobra.Command{
		Use:   "list [source...]",
		Short: "List the definitions in the configured sources",
		Long: `List the definitions in every configured source, or in those named: the
objects each one defines, read from the version of the source that was last
fetched. For devicetype-library, that is its device types, and a manufacturer
for each name they give, with the slug NetBox's web UI would make from the
name, which TrueUp uses only to create the manufacturer.

Files that fail to read are reported with their path, line and reason, and
skipped: YAML that isn't valid, or that uses what TrueUp doesn't read, such
as aliases or tags; a file that fails the library's own JSON schemas, from
the same version of the source; and values NetBox would refuse. Warnings
point at what TrueUp reads but NetBox may not take as it is, such as two
manufacturers that would get the same slug.

The source isn't fetched, and nothing is written, unless --fetch is given:
then each source is fetched first, as "source fetch" fetches it, and the
fetch is recorded in TrueUp's database. A source whose fetch fails isn't
listed, even if an earlier version is in the cache: list without --fetch
lists that.

Exit codes:
  0  Every source was read, and every file in it is valid.
  1  The command couldn't run, such as when, with --fetch, the data
     directory can't be written to, or the database can't be opened or
     written.
  2  A name given isn't a configured source.
  3  The configuration is invalid, no sources are configured, or, with
     --fetch, database.url names a database this release can't use.
  4  Some sources couldn't be read, such as one never fetched, whose
     cache can't be read or, with --fetch, whose fetch failed, or some
     files are invalid. The result is printed
     as usual, with each problem.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.loadConfig(cmd)
			if err != nil {
				return err
			}
			sources, err := chooseSources(cfg.Settings.Sources, args)
			if err != nil {
				return err
			}
			fetchErrors := map[string]string{}
			if fetch {
				fetched, err := a.fetchSources(cmd.Context(), cfg, sources)
				if err != nil {
					return err
				}
				for _, s := range fetched.Sources {
					if s.Error != nil {
						fetchErrors[s.Name] = *s.Error
					}
				}
			}
			result, err := a.listSources(cmd.Context(), cfg, sources, fetchErrors)
			if err != nil {
				return err
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
	return cmd
}

// listJSON is the result of `list`, printed as text or JSON. Its JSON fields
// are part of the CLI's public interface (ADR 0018).
type listJSON struct {
	OK      bool             `json:"ok"` // every source was read, and every file is valid
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
	Files        int              `json:"files"`
	InvalidFiles int              `json:"invalid_files"`
	Definitions  []definitionJSON `json:"definitions"`
	Problems     []problemJSON    `json:"problems"`
}

type definitionJSON struct {
	// Type is the object type, such as dcim.devicetype.
	Type string `json:"type"`
	// Identity holds the fields that tell the object from others of its
	// type, such as a device type's manufacturer and model.
	Identity map[string]model.Value `json:"identity"`
	// Path is the file that defines the object, or null for one derived
	// from other files, which DerivedFrom lists.
	Path        *string  `json:"path"`
	DerivedFrom []string `json:"derived_from"`
	// Fields are the fields the definition sets, as NetBox's API writes
	// them: a reference as the natural key of what it refers to.
	Fields map[string]model.Value `json:"fields"`
	// Derived lists fields made rather than read, which TrueUp uses only
	// to create the object.
	Derived []string `json:"derived"`
	// Metadata holds what the file says that isn't a NetBox field.
	Metadata map[string]model.Value `json:"metadata"`
	// Unmapped counts the lists of children the file holds that TrueUp
	// doesn't read yet, such as a device type's interfaces.
	Unmapped map[string]int `json:"unmapped"`
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
	invalid := 0
	for _, s := range r.Sources {
		if s.Error != nil {
			failed = append(failed, s.Name)
		}
		invalid += s.InvalidFiles
	}
	var parts []string
	if len(failed) > 0 {
		parts = append(parts, fmt.Sprintf("%d of the %d sources couldn't be read: %s", len(failed), len(r.Sources), strings.Join(failed, ", ")))
	}
	switch {
	case invalid == 1:
		parts = append(parts, "1 file is invalid")
	case invalid > 1:
		parts = append(parts, fmt.Sprintf("%d files are invalid", invalid))
	}
	return strings.Join(parts, "; ")
}

// listSources reads each source's last fetched version. A source that
// can't be read is reported in the result, with fetchErrors giving why for
// those whose fetch failed; only an interrupt is an error.
func (a *app) listSources(ctx context.Context, cfg *config.Config, sources []config.Source, fetchErrors map[string]string) (listJSON, error) {
	kinds := a.sourceKinds(cachePath(cfg))
	result := listJSON{OK: true, Sources: []listSourceJSON{}}
	for _, s := range sources {
		row := listSourceJSON{Name: s.Name, Format: s.Format, Definitions: []definitionJSON{}, Problems: []problemJSON{}}
		fail := func(msg string) {
			row.OK, row.Error = false, &msg
			a.logger.Warn("can't read source", "source", s.Name, "error", msg)
		}
		if msg, ok := fetchErrors[s.Name]; ok {
			fail("the fetch failed: " + msg)
		} else if err := a.listSource(ctx, kinds, s, &row); err != nil {
			if ctx.Err() != nil {
				return listJSON{}, fmt.Errorf("interrupted while reading source %s: %w", s.Name, ctx.Err())
			}
			fail(err.Error())
		}
		result.OK = result.OK && row.OK
		result.Sources = append(result.Sources, row)
	}
	return result, nil
}

// listSource reads one source's last fetched version into its row.
func (a *app) listSource(ctx context.Context, kinds map[string]source.Kind, s config.Source, row *listSourceJSON) error {
	read, err := a.readSource(ctx, kinds, s)
	if err != nil {
		return err
	}
	res := read.result
	row.Ref, row.Version = &read.ref, &read.version
	row.Files, row.InvalidFiles = res.Files, res.Invalid
	row.OK = res.Invalid == 0
	for _, d := range res.Definitions {
		row.Definitions = append(row.Definitions, definitionOf(d))
	}
	row.Problems = problemsOf(res.Problems)
	return nil
}

// sourceRead is what a source's format read from its last fetched version.
type sourceRead struct {
	ref, version string
	result       *format.Result
}

// readSource reads a source's last fetched version with its format, from
// the cache, without touching the network or the database.
func (a *app) readSource(ctx context.Context, kinds map[string]source.Kind, s config.Source) (sourceRead, error) {
	kind, ok := kinds[s.Kind]
	if !ok {
		return sourceRead{}, fmt.Errorf("TrueUp can't read sources of kind %q", s.Kind)
	}
	spec, f, err := sourceSpec(s)
	if err != nil {
		return sourceRead{}, err
	}
	// Walk, and hash, only the files the format reads, not the library's
	// images.
	if spec.Only, err = glob.CompileAll(f.Reads()); err != nil {
		return sourceRead{}, fmt.Errorf("format %s: %w", f.Name(), err)
	}
	snap, err := source.Open(ctx, kind, spec)
	if errors.Is(err, source.ErrNotFetched) {
		// The kind says why, such as that the source's url changed since.
		return sourceRead{}, fmt.Errorf("%w: run 'netbox-trueup-ai source fetch', or use --fetch", err)
	}
	if err != nil {
		return sourceRead{}, fmt.Errorf("open the cached version: %w", err)
	}
	defer func() {
		if err := snap.Close(); err != nil {
			a.logger.Warn("can't close the source's cache", "source", s.Name, "error", err)
		}
	}()
	res, err := f.Read(ctx, snap)
	if err != nil {
		return sourceRead{}, fmt.Errorf("format %s: %w", f.Name(), err)
	}
	return sourceRead{ref: snap.Ref, version: snap.Version, result: res}, nil
}

func definitionOf(d format.Definition) definitionJSON {
	out := definitionJSON{
		Type:        d.Object.Type,
		Identity:    d.Identity,
		DerivedFrom: d.DerivedFrom,
		Fields:      d.Object.Fields,
		Derived:     d.Derived,
		Metadata:    d.Metadata,
		Unmapped:    d.Unmapped,
	}
	if d.Path != "" {
		out.Path = &d.Path
	}
	// Empty rather than null, so that scripts needn't check.
	if out.DerivedFrom == nil {
		out.DerivedFrom = []string{}
	}
	if out.Derived == nil {
		out.Derived = []string{}
	}
	if out.Metadata == nil {
		out.Metadata = map[string]model.Value{}
	}
	if out.Unmapped == nil {
		out.Unmapped = map[string]int{}
	}
	return out
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
		for _, d := range s.Definitions {
			count[d.Type]++
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
	byManufacturer := map[string][]definitionJSON{}
	var manufacturers []definitionJSON
	for _, d := range s.Definitions {
		switch d.Type {
		case "dcim.manufacturer":
			manufacturers = append(manufacturers, d)
		default:
			name := text(d.Identity["manufacturer"])
			byManufacturer[name] = append(byManufacturer[name], d)
		}
	}
	var lines []string
	row := func(d definitionJSON, manufacturer, model string) {
		slug := text(d.Fields["slug"])
		if slices.Contains(d.Derived, "slug") {
			slug += " (derived)"
		}
		from := "-"
		switch {
		case d.Path != nil:
			from = *d.Path
		case len(d.DerivedFrom) == 1:
			from = "1 file"
		case len(d.DerivedFrom) > 1:
			from = fmt.Sprintf("%d files", len(d.DerivedFrom))
		}
		typ := typeNames[d.Type]
		if typ == "" {
			typ = d.Type
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
	// Definitions whose manufacturer the source doesn't define, which
	// devicetype-library's format never gives, are still listed.
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
