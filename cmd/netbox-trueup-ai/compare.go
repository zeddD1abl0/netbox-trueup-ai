package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/compare"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/format"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model/dcim"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/netbox"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store"
)

func newCompareCommand(a *app) *cobra.Command {
	var (
		out           output
		fetch         bool
		signal        bool
		manufacturers []string
	)
	cmd := &cobra.Command{
		Use:   "compare [source...]",
		Short: "Compare the definitions in the configured sources with NetBox",
		Long: `Compare the definitions in every configured source, or in those named,
with what NetBox holds, read from the version of each source that was last
fetched. For devicetype-library, that is its manufacturers and device types;
their component templates aren't compared yet.

Each definition is matched to NetBox's objects by NetBox's own uniqueness
rules, exactly, case included: a manufacturer's name and slug, and a device
type's manufacturer and model, and manufacturer and slug. Its outcome is:

  candidate  Its identity (a manufacturer's name, a device type's
             manufacturer and model) matches one NetBox object, and nothing
             else collides. Importing it would link it to that object.
  absent     Nothing in NetBox matches. Importing it would create it.
  conflict   It collides with a NetBox object that isn't its match, such as
             a device type whose model matches one device type and whose
             slug another, so NetBox couldn't hold both. A person decides.

A candidate is compared field by field, as NetBox stores each value. Only
the fields a definition sets count: NetBox's other fields are shown, but
aren't differences. A value the format derived rather than read, such as a
manufacturer's slug, is used only to create the object, so it never differs
from one NetBox has.

The comparison of each definition is recorded in TrueUp's database, as the
latest, against the version of the source compared. NetBox is only read.
The token sees what its permissions let it see: where they have
constraints, the objects they hide show as absent.

The source isn't fetched unless --fetch is given: then each source is
fetched first, as "source fetch" fetches it.

Exit codes:
  0  Every source was read and its comparisons recorded, and every file in
     it is valid, whatever the outcomes.
  1  The command couldn't run, such as when NetBox can't be reached, runs a
     release TrueUp doesn't support, refuses the token, or the token can't
     read manufacturers or device types ('netbox-trueup-ai netbox check'
     says which), or when the database can't be opened or written.
  2  A name given isn't a configured source.
  3  The configuration is invalid, no sources are configured, netbox.url
     or netbox.token isn't set, or database.url names a database this
     release can't use.
  4  Some sources couldn't be read, such as one never fetched, or their
     comparisons couldn't be recorded, because the source was fetched again
     while it was compared, or some files are invalid, or no source read
     defines a manufacturer --manufacturer names. The rest is compared, and
     the result printed as usual.
  5  Only with --exit-code: everything was read, and some definitions
     aren't in step with NetBox: they are absent, in conflict, or differ.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.loadConfig(cmd)
			if err != nil {
				return err
			}
			sources, err := chooseSources(cfg.Settings.Sources, args)
			if err != nil {
				return err
			}
			if err := requireNetBox(cfg.Settings.NetBox); err != nil {
				return err
			}
			result, err := a.runCompare(cmd.Context(), cfg, sources, fetch, manufacturers)
			if err != nil {
				return err
			}
			if out == outputJSON {
				err = writeJSON(cmd.OutOrStdout(), result)
			} else {
				err = writeCompareText(cmd.OutOrStdout(), result)
			}
			if err != nil {
				return err
			}
			if !result.OK {
				return outcomeError{code: exitCompareProblems, err: errors.New(result.summary())}
			}
			if n := result.notInStep(); signal && n > 0 {
				return outcomeError{code: exitNotInStep, err: fmt.Errorf("%d of the %d definitions compared aren't in step with NetBox", n, len(result.results))}
			}
			return nil
		},
	}
	addOutputFlag(cmd, &out)
	cmd.Flags().BoolVar(&fetch, "fetch", false, "fetch each source first, as source fetch does")
	cmd.Flags().BoolVar(&signal, "exit-code", false, "exit with 5 when some definitions aren't in step with NetBox")
	cmd.Flags().StringArrayVar(&manufacturers, "manufacturer", nil,
		"compare only this manufacturer, by its exact name, and its device types; repeat it for more")
	return cmd
}

// compareJSON is the result of `compare`, printed as text or JSON. Its JSON
// fields are part of the CLI's public interface (ADR 0018), and so are the
// objects', which the database records too.
type compareJSON struct {
	// OK says every source was read and its comparisons recorded, and
	// every file is valid.
	OK         bool                `json:"ok"`
	NetBox     compareNetBoxJSON   `json:"netbox"`
	ComparedAt time.Time           `json:"compared_at"`
	Sources    []compareSourceJSON `json:"sources"`
	// Objects are the comparisons of the definitions, source by source, in
	// the order each source's format gives them, as JSON, which the
	// database records too.
	Objects  []json.RawMessage `json:"objects"`
	Warnings []warningJSON     `json:"warnings"`

	// results are the Objects, for text output, and unknown the names
	// given to --manufacturer that no source read defines.
	results []compare.Result
	unknown []string
}

type compareNetBoxJSON struct {
	URL     string `json:"url"`
	Version string `json:"version"`
}

type compareSourceJSON struct {
	Name   string `json:"name"`
	Format string `json:"format"`
	// Ref and Version are those of the version read, or null when the
	// source couldn't be read.
	Ref     *string `json:"ref"`
	Version *string `json:"version"`
	// OK says the source was read and its comparisons recorded, and every
	// file in it is valid.
	OK bool `json:"ok"`
	// Error says why the source couldn't be read, or its comparisons
	// recorded, or is null.
	Error        *string `json:"error"`
	Files        int     `json:"files"`
	InvalidFiles int     `json:"invalid_files"`
	// Definitions is how many definitions the source holds, and Compared
	// how many of them were compared, which --manufacturer chooses. The
	// rest count the outcomes of those compared.
	Definitions int           `json:"definitions"`
	Compared    int           `json:"compared"`
	InStep      int           `json:"in_step"`
	Differing   int           `json:"differing"`
	Absent      int           `json:"absent"`
	Conflicts   int           `json:"conflicts"`
	Problems    []problemJSON `json:"problems"`
}

// summary says what went wrong, for the error after the result.
func (r compareJSON) summary() string {
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
		parts = append(parts, fmt.Sprintf("%d of the %d sources couldn't be read or recorded: %s", len(failed), len(r.Sources), strings.Join(failed, ", ")))
	}
	switch {
	case invalid == 1:
		parts = append(parts, "1 file is invalid")
	case invalid > 1:
		parts = append(parts, fmt.Sprintf("%d files are invalid", invalid))
	}
	if len(r.unknown) > 0 {
		quoted := make([]string, len(r.unknown))
		for i, name := range r.unknown {
			quoted[i] = strconv.Quote(name)
		}
		parts = append(parts, "no source read defines the manufacturer "+strings.Join(quoted, ", "))
	}
	return strings.Join(parts, "; ")
}

// notInStep counts the definitions compared that aren't in step with NetBox.
func (r compareJSON) notInStep() int {
	n := 0
	for _, o := range r.results {
		if !o.InStep() {
			n++
		}
	}
	return n
}

// sourceCompared is one source read for a comparison.
type sourceCompared struct {
	row *compareSourceJSON
	// defined are the keys of all the source's definitions, chosen the
	// definitions compared, and chosenKeys their keys.
	defined    []store.ComparisonKey
	chosen     []compare.Definition
	chosenKeys []store.ComparisonKey
}

// runCompare opens the database, connects to NetBox, fetches the sources if
// asked, compares them with NetBox, and records the comparisons. It closes
// the database before it returns, so that nothing is printed if closing it
// fails.
func (a *app) runCompare(ctx context.Context, cfg *config.Config, sources []config.Source, fetch bool, manufacturers []string) (_ compareJSON, err error) {
	db, _, err := a.openStore(ctx, cfg)
	if err != nil {
		return compareJSON{}, err
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	// NetBox first, so that a NetBox that can't be reached, or that refuses
	// the token, stops the command before a fetch that may take minutes.
	c, err := connectNetBox(ctx, cfg, a.logger)
	if err != nil {
		return compareJSON{}, err
	}
	fetchErrors := map[string]string{}
	if fetch {
		root, err := cacheRoot(cfg)
		if err != nil {
			return compareJSON{}, err
		}
		fetched, err := a.fetchInto(ctx, db, cfg, root, sources)
		if err != nil {
			return compareJSON{}, err
		}
		for _, s := range fetched.Sources {
			if s.Error != nil {
				fetchErrors[s.Name] = *s.Error
			}
		}
	}
	return a.compareSources(ctx, cfg, db, c, sources, fetchErrors, manufacturers)
}

// compareSources reads each source's last fetched version, compares the
// definitions chosen with NetBox, and records the comparisons. A source that
// can't be read, or whose comparisons can't be recorded, is reported in the
// result; NetBox and the database failing are errors.
func (a *app) compareSources(ctx context.Context, cfg *config.Config, db store.Store, c *netbox.Client, sources []config.Source,
	fetchErrors map[string]string, manufacturers []string,
) (compareJSON, error) {
	chosen := map[string]bool{}
	for _, name := range manufacturers {
		chosen[name] = true
	}
	kinds := a.sourceKinds(cachePath(cfg))
	result := compareJSON{OK: true, Sources: []compareSourceJSON{}, Objects: []json.RawMessage{}, Warnings: []warningJSON{}}
	var compared []sourceCompared
	named := map[string]bool{}
	for _, s := range sources {
		row := &compareSourceJSON{Name: s.Name, Format: s.Format, Problems: []problemJSON{}}
		sc := sourceCompared{row: row}
		if msg, ok := fetchErrors[s.Name]; ok {
			a.failSource(row, "the fetch failed: "+msg)
		} else if read, err := a.readSource(ctx, kinds, s); err != nil {
			if ctx.Err() != nil {
				return compareJSON{}, fmt.Errorf("interrupted while reading source %s: %w", s.Name, ctx.Err())
			}
			a.failSource(row, err.Error())
		} else {
			res := read.result
			row.Ref, row.Version = &read.ref, &read.version
			row.Files, row.InvalidFiles, row.Definitions = res.Files, res.Invalid, len(res.Definitions)
			row.OK = res.Invalid == 0
			row.Problems = problemsOf(res.Problems)
			for _, d := range res.Definitions {
				k, err := comparisonKey(d)
				if err != nil {
					return compareJSON{}, fmt.Errorf("source %s: %w", s.Name, err)
				}
				sc.defined = append(sc.defined, k)
				name, ok := manufacturerName(d)
				if ok {
					named[name] = true
				}
				if len(chosen) == 0 || ok && chosen[name] {
					sc.chosen = append(sc.chosen, compare.Definition{Source: s.Name, Definition: d})
					sc.chosenKeys = append(sc.chosenKeys, k)
				}
			}
			row.Compared = len(sc.chosen)
		}
		compared = append(compared, sc)
	}
	// A name nothing defines, such as a typing mistake, leaves the
	// comparison short of what was asked for, so the run isn't OK.
	invalid := slices.ContainsFunc(compared, func(sc sourceCompared) bool { return sc.row.InvalidFiles > 0 })
	for _, name := range slices.Compact(slices.Sorted(slices.Values(manufacturers))) {
		if !named[name] {
			a.logger.Warn("no source read defines the manufacturer", "manufacturer", name)
			msg := "no source read defines the manufacturer " + strconv.Quote(name)
			if invalid {
				msg += "; some files are invalid, and one of them may"
			}
			result.Warnings = append(result.Warnings, warningJSON{Message: msg})
			result.OK = false
			result.unknown = append(result.unknown, name)
		}
	}

	var defs []compare.Definition
	for _, sc := range compared {
		defs = append(defs, sc.chosen...)
	}
	var results []compare.Result
	if len(defs) > 0 {
		observed, err := readNetBox(ctx, c, defs)
		if err != nil {
			return compareJSON{}, err
		}
		if results, err = compare.Compare(dcim.Specs(), defs, observed); err != nil {
			return compareJSON{}, fmt.Errorf("compare with NetBox: %w", err)
		}
	}
	info := c.Info()
	result.NetBox = compareNetBoxJSON{URL: info.URL, Version: info.Version}
	// To the microsecond, as the database records it.
	result.ComparedAt = time.Now().UTC().Truncate(time.Microsecond)

	for _, sc := range compared {
		if sc.row.Version == nil {
			result.OK = false
			result.Sources = append(result.Sources, *sc.row)
			continue
		}
		// Compare gives a result for each definition, in their order.
		if len(results) < len(sc.chosen) {
			return compareJSON{}, fmt.Errorf("compare with NetBox: %d results for the %d definitions of source %s", len(results), len(sc.chosen), sc.row.Name)
		}
		mine := results[:len(sc.chosen)]
		results = results[len(sc.chosen):]
		for i, r := range mine {
			if d := sc.chosen[i]; r.Type != d.Object.Type || r.Columns[0].Source != d.Source || r.Columns[0].Path != d.Path {
				return compareJSON{}, fmt.Errorf("compare with NetBox: result %d of source %s is of another definition", i, sc.row.Name)
			}
		}
		// Each result is written as JSON once, for the database and for
		// the output alike.
		written := make([]json.RawMessage, len(mine))
		for i, r := range mine {
			out, err := json.Marshal(r)
			if err != nil {
				return compareJSON{}, fmt.Errorf("source %s: write the comparison of %s: %w", sc.row.Name, sc.chosenKeys[i], err)
			}
			written[i] = out
		}
		if err := recordComparisons(ctx, db, sc, mine, written, result); err != nil {
			if !errors.Is(err, store.ErrSnapshotChanged) {
				return compareJSON{}, err
			}
			// No quote before the error, which can hold JSON, so that the
			// message can't read as the error quoted (CodeQL's
			// go/unsafe-quoting).
			a.failSource(sc.row, fmt.Sprintf("its comparisons were not recorded: %v; run 'netbox-trueup-ai source fetch', or use --fetch, and compare again", err))
		}
		for _, r := range mine {
			switch {
			case r.InStep():
				sc.row.InStep++
			case r.Outcome == compare.Candidate:
				sc.row.Differing++
			case r.Outcome == compare.Absent:
				sc.row.Absent++
			case r.Outcome == compare.Conflict:
				sc.row.Conflicts++
			}
		}
		result.OK = result.OK && sc.row.OK
		result.Sources = append(result.Sources, *sc.row)
		result.Objects = append(result.Objects, written...)
		result.results = append(result.results, mine...)
	}
	if len(results) > 0 {
		return compareJSON{}, fmt.Errorf("compare with NetBox: %d results of no definition", len(results))
	}
	return result, nil
}

func (a *app) failSource(row *compareSourceJSON, msg string) {
	row.OK, row.Error = false, &msg
	a.logger.Warn("can't compare source", "source", row.Name, "error", msg)
}

// recordComparisons records one source's comparisons, results written as
// JSON, as the latest of its definitions, and removes those of definitions
// it no longer holds.
func recordComparisons(ctx context.Context, db store.Store, sc sourceCompared, results []compare.Result, written []json.RawMessage, run compareJSON) error {
	rec := store.ComparisonRun{
		Source:        sc.row.Name,
		SourceVersion: *sc.row.Version,
		ComparedAt:    run.ComparedAt,
		NetBoxURL:     run.NetBox.URL,
		NetBoxVersion: run.NetBox.Version,
		Defined:       sc.defined,
	}
	for i, r := range results {
		rec.Comparisons = append(rec.Comparisons, store.Comparison{
			ComparisonKey: sc.chosenKeys[i], Outcome: string(r.Outcome), NetBoxID: r.NetBoxID, Differences: r.Differences, Result: written[i],
		})
	}
	return db.RecordComparisons(ctx, rec)
}

func comparisonKey(d format.Definition) (store.ComparisonKey, error) {
	identity, err := json.Marshal(d.Identity)
	if err != nil {
		return store.ComparisonKey{}, fmt.Errorf("write the identity of a %s: %w", d.Object.Type, err)
	}
	return store.ComparisonKey{ObjectType: d.Object.Type, Path: d.Path, Identity: string(identity)}, nil
}

// manufacturerName returns the name of the manufacturer a definition is, or
// that a device type's refers to.
func manufacturerName(d format.Definition) (string, bool) {
	var name model.Value
	switch d.Object.Type {
	case "dcim.manufacturer":
		name = d.Identity["name"]
	case "dcim.devicetype":
		ref, ok := d.Identity["manufacturer"].(model.Reference)
		if !ok {
			return "", false
		}
		name = ref.Key["name"]
	}
	s, ok := name.(model.String)
	return string(s), ok
}

// readNetBox reads the NetBox objects the definitions could match or collide
// with: every manufacturer, since a manufacturer's name and slug are each
// unique among all of them, and the device types of the manufacturers that
// the definitions name and NetBox has, since both of a device type's
// uniqueness rules include its manufacturer. Each object is read through
// its spec, and one that can't be is an error, since leaving it out would
// make what it is show as absent.
func readNetBox(ctx context.Context, c *netbox.Client, defs []compare.Definition) ([]compare.Observed, error) {
	specs := dcim.Specs()
	manufacturer, deviceType := model.SpecOf(specs, "dcim.manufacturer"), model.SpecOf(specs, "dcim.devicetype")
	if manufacturer == nil || deviceType == nil {
		return nil, errors.New("TrueUp has no spec of manufacturers or device types")
	}
	var observed []compare.Observed
	decode := func(s *model.Spec) func(json.RawMessage) error {
		return func(raw json.RawMessage) error {
			id, obj, err := s.DecodeAPI(specs, raw)
			if err != nil {
				return fmt.Errorf("NetBox's answer can't be read: %w", err)
			}
			observed = append(observed, compare.Observed{ID: id, Object: obj})
			return nil
		}
	}
	if err := c.List(ctx, manufacturer.Endpoint, fieldsQuery(manufacturer), decode(manufacturer)); err != nil {
		return nil, readError(manufacturer, err)
	}

	// The manufacturers the definitions refer to, as the comparison will
	// resolve them.
	ids, err := compare.Referenced(specs, defs, observed, manufacturer.Type)
	if err != nil {
		return nil, fmt.Errorf("compare with NetBox: %w", err)
	}
	if len(ids) > 0 {
		if err := c.ListByParent(ctx, deviceType.Endpoint, "manufacturer", ids, fieldsQuery(deviceType), decode(deviceType)); err != nil {
			return nil, readError(deviceType, err)
		}
	}
	return observed, nil
}

// fieldsQuery asks NetBox for an object's ID and the fields its spec
// describes, and nothing else. NetBox leaves out a name it doesn't know,
// which DecodeAPI then reports.
func fieldsQuery(s *model.Spec) url.Values {
	names := []string{"id"}
	for _, f := range s.Fields {
		if f.Ref != nil && f.Ref.Shape == model.ShapeTypeAndID {
			names = append(names, f.Ref.TypeField, f.Ref.IDField)
			continue
		}
		names = append(names, f.Name)
	}
	return url.Values{"fields": {strings.Join(names, ",")}}
}

// readError says what reading an object type from NetBox failed on, and
// for a token that can't read it, what to do.
func readError(s *model.Spec, err error) error {
	if se, ok := errors.AsType[*netbox.StatusError](err); ok && se.StatusCode == http.StatusForbidden && se.FromNetBox() {
		return fmt.Errorf("the token can't read %s (%w): grant its user the view permission on it; 'netbox-trueup-ai netbox check' lists what it can read", s.Type, err)
	}
	return fmt.Errorf("read %s from NetBox: %w", s.Type, err)
}

func writeCompareText(w io.Writer, r compareJSON) error {
	var b strings.Builder
	fmt.Fprintf(&b, "NetBox:  %s, release %s\n\n", cell(r.NetBox.URL), cell(r.NetBox.Version))
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "SOURCE\tREF\tVERSION\tCOMPARED\tIN STEP\tDIFFERING\tABSENT\tCONFLICTS\tINVALID\tWARNINGS")
	for _, s := range r.Sources {
		if s.Version == nil {
			_, _ = fmt.Fprintf(tw, "%s\t-\t-\t-\t-\t-\t-\t-\t-\t-\n", s.Name)
			continue
		}
		warnings := 0
		for _, p := range s.Problems {
			if p.Severity == string(format.Warning) {
				warnings++
			}
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\n", s.Name, cell(*s.Ref), cell(*s.Version),
			s.Compared, s.InStep, s.Differing, s.Absent, s.Conflicts, s.InvalidFiles, warnings)
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("write output: %w", err)
	}

	var notInStep []compare.Result
	for _, o := range r.results {
		if !o.InStep() {
			notInStep = append(notInStep, o)
		}
	}
	if len(notInStep) > 0 {
		b.WriteString("\n")
		tw = tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "SOURCE\tOUTCOME\tTYPE\tMANUFACTURER\tMODEL\tNETBOX\tDIFFERENCES\tFROM")
		for _, o := range notInStep {
			def := o.Columns[0]
			manufacturer, modelName := identityCells(o.Type, o.Identity)
			netboxID, differences := "-", "-"
			if o.NetBoxID != 0 {
				netboxID, differences = "#"+strconv.FormatInt(o.NetBoxID, 10), strconv.Itoa(o.Differences)
			}
			_, _ = fmt.Fprintln(tw, strings.Join([]string{
				def.Source, string(o.Outcome), typeName(o.Type), cell(manufacturer), cell(modelName), netboxID, differences, cell(fromText(def)),
			}, "\t"))
		}
		if err := tw.Flush(); err != nil {
			return fmt.Errorf("write output: %w", err)
		}
	}

	var conflicts []string
	for _, o := range r.results {
		if o.Outcome != compare.Conflict {
			continue
		}
		var parts []string
		for _, u := range o.Unresolved {
			if len(u.Candidates) == 0 {
				continue
			}
			ids := make([]string, len(u.Candidates))
			for i, id := range u.Candidates {
				ids[i] = "#" + strconv.FormatInt(id, 10)
			}
			parts = append(parts, fmt.Sprintf("its %s, %s, could be any of NetBox's %ss %s, which NetBox should keep unique",
				u.Field, strconv.Quote(text(u.Ref)), typeName(u.Ref.Type), strings.Join(ids, ", ")))
		}
		for _, c := range o.Collisions {
			verb := "matches"
			if len(c.Rule) > 1 {
				verb = "match"
			}
			parts = append(parts, fmt.Sprintf("its %s %s NetBox's %s #%d, %s",
				strings.Join(c.Rule, " and "), verb, typeName(o.Type), c.ID, identityText(o.Type, c.Identity)))
		}
		conflicts = append(conflicts, "  "+cell(objectText(o)+": "+strings.Join(parts, "; ")))
	}
	if len(conflicts) > 0 {
		b.WriteString("\nConflicts:\n" + strings.Join(conflicts, "\n") + "\n")
	}

	first := true
	for _, o := range r.results {
		if o.NetBoxID == 0 || o.Differences == 0 {
			continue
		}
		if first {
			b.WriteString("\nDifferences:\n")
			first = false
		}
		fmt.Fprintf(&b, "  %s, NetBox #%d:\n", cell(objectText(o)), o.NetBoxID)
		tw = tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "    FIELD\tDEFINITION\tNETBOX")
		for _, row := range o.Rows {
			if row.Differs && row.Managed {
				_, _ = fmt.Fprintf(tw, "    %s\t%s\t%s\n", cell(row.Path.String()), cell(valueText(row.Cells[0])), cell(valueText(row.Cells[1])))
			}
		}
		if err := tw.Flush(); err != nil {
			return fmt.Errorf("write output: %w", err)
		}
	}

	var warnings []string
	for _, w := range r.Warnings {
		warnings = append(warnings, "  "+cell(w.Message))
	}
	for _, s := range r.Sources {
		for _, p := range s.Problems {
			if p.Severity == string(format.Warning) {
				warnings = append(warnings, "  "+s.Name+": "+cell(problemText(p)))
			}
		}
	}
	if len(warnings) > 0 {
		b.WriteString("\nWarnings:\n" + strings.Join(warnings, "\n") + "\n")
	}
	var invalid, errs []string
	for _, s := range r.Sources {
		for _, p := range s.Problems {
			if p.Severity == string(format.Error) {
				invalid = append(invalid, "  "+s.Name+": "+cell(problemText(p)))
			}
		}
		if s.Error != nil {
			errs = append(errs, "  "+s.Name+": "+cell(*s.Error))
		}
	}
	if len(invalid) > 0 {
		b.WriteString("\nInvalid files:\n" + strings.Join(invalid, "\n") + "\n")
	}
	if len(errs) > 0 {
		b.WriteString("\nErrors:\n" + strings.Join(errs, "\n") + "\n")
	}
	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

func typeName(typ string) string {
	if name := typeNames[typ]; name != "" {
		return name
	}
	return typ
}

// identityCells returns an object's manufacturer and model for a table, or
// "-" where it has none.
func identityCells(typ string, identity map[string]model.Value) (manufacturer, modelName string) {
	if typ == "dcim.manufacturer" {
		return text(identity["name"]), "-"
	}
	return text(identity["manufacturer"]), text(identity["model"])
}

// identityText names an object by its identity, such as Dell "PowerEdge
// R640" for a device type.
func identityText(typ string, identity map[string]model.Value) string {
	manufacturer, modelName := identityCells(typ, identity)
	if typ == "dcim.manufacturer" {
		return strconv.Quote(manufacturer)
	}
	return manufacturer + " " + strconv.Quote(modelName)
}

// objectText names a compared definition: its source, type, identity and
// file.
func objectText(o compare.Result) string {
	def := o.Columns[0]
	return fmt.Sprintf("%s: %s %s (%s)", def.Source, typeName(o.Type), identityText(o.Type, o.Identity), fromText(def))
}

// fromText says where a definition comes from: its file, or how many files
// it is derived from.
func fromText(c compare.Column) string {
	switch {
	case c.Path != "":
		return c.Path
	case len(c.DerivedFrom) == 1:
		return "1 file"
	case len(c.DerivedFrom) > 1:
		return fmt.Sprintf("%d files", len(c.DerivedFrom))
	}
	return "-"
}

// valueText writes a value for the differences' table: text and choices
// quoted, a reference by its name and ID, and "not set" for a field a
// version leaves out.
func valueText(v model.Value) string {
	switch v := v.(type) {
	case nil:
		return "not set"
	case model.Reference:
		name := strconv.Quote(text(v))
		if v.ID != 0 {
			return fmt.Sprintf("%s (#%d)", name, v.ID)
		}
		return name
	}
	return v.String()
}
