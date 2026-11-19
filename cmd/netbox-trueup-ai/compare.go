package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
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
		out    output
		fetch  bool
		signal bool
		sel    compareSelection
	)
	cmd := &cobra.Command{
		Use:   "compare [source...]",
		Short: "Compare the definitions in the configured sources with NetBox",
		Long: `Compare the definitions in every configured source, or in those named,
with what NetBox holds, read from the version of each source that was last
fetched. For devicetype-library, that is its manufacturers and device types,
with their console, power, interface, port and bay templates, and each front
port's mappings to rear ports. Inventory items, which NetBox has deprecated,
aren't compared.

The definitions are read from TrueUp's database, where "source fetch" loads
each version it fetches. A version the database holds nothing of, or what
another release of TrueUp or other settings of the source read, is loaded
first, which takes a few seconds for devicetype-library, after waiting for
another process's fetch of the source, if one is running.

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

What is compared:

  By default, what NetBox has: the candidates and conflicts. The
  definitions NetBox hasn't got are only counted.
  --manufacturer and --model compare what they name, absent included: a
  manufacturer and its device types, and the device types with a model,
  with their manufacturers. Together, a manufacturer's device types with
  that model.
  --all compares every definition.

A name is matched as NetBox would store it, without white space at either
end, and otherwise exactly, case included.

A candidate is compared field by field, as NetBox stores each value. Only
the fields a definition sets count: NetBox's other fields are shown, but
aren't differences. A value the format derived rather than read, such as a
manufacturer's slug, is used only to create the object, so it never differs
from one NetBox has.

A device type's component templates are matched with NetBox's by name, within
the device type, exactly, and compared field by field in the same way. A
template the definition has and NetBox hasn't is one difference. A template
only NetBox has is listed, but isn't a difference: TrueUp never removes one.

The comparison of each definition compared is recorded in TrueUp's database,
as the latest, against the version of the source compared. By default, and
with --all, the source's other comparisons are removed; with --manufacturer
or --model, they are kept. NetBox is only read. The token sees what its
permissions let it see: where they have constraints, the objects they hide
show as absent.

The source isn't fetched unless --fetch is given: then each source is
fetched first, as "source fetch" fetches it.

Exit codes:
  0  Every source was read and its comparisons recorded, and every file in
     it is valid, whatever the outcomes.
  1  The command couldn't run, such as when NetBox can't be reached, runs a
     release TrueUp doesn't support, refuses the token, or the token can't
     read manufacturers, device types or the component templates compared
     ('netbox-trueup-ai netbox check' says which), when NetBox changes while
     it is read, such as a device type deleted, or when the database can't
     be opened or written.
  2  A name given isn't a configured source, or --all is given with
     --manufacturer or --model.
  3  The configuration is invalid, no sources are configured, netbox.url
     or netbox.token isn't set, or database.url names a database this
     release can't use.
  4  Some sources couldn't be read, such as one never fetched, or their
     comparisons couldn't be recorded, because the source was fetched or
     loaded again while it was compared, or some files are invalid, or no
     source read defines a manufacturer --manufacturer names, or a model
     --model names. The rest is compared, and the result printed as usual.
  5  Only with --exit-code: everything was read, and some definitions
     compared aren't in step with NetBox: they are absent, in conflict, or
     differ.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if sel.all && (len(sel.manufacturers) > 0 || len(sel.models) > 0) {
				return usageError{errors.New("--all compares every definition, so it can't be given with --manufacturer or --model")}
			}
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
			result, err := a.runCompare(cmd.Context(), cfg, sources, fetch, sel, out != outputJSON)
			if err != nil {
				return err
			}
			if out == outputJSON {
				err = writeCompareJSON(cmd.OutOrStdout(), result)
			} else {
				err = writeCompareText(cmd.OutOrStdout(), result)
			}
			if err != nil {
				return err
			}
			if !result.OK {
				return outcomeError{code: exitCompareProblems, err: errors.New(result.summary())}
			}
			if compared, inStep := result.counts(); signal && compared > inStep {
				return outcomeError{code: exitNotInStep, err: fmt.Errorf("%d of the %d definitions compared aren't in step with NetBox", compared-inStep, compared)}
			}
			return nil
		},
	}
	addOutputFlag(cmd, &out)
	cmd.Flags().BoolVar(&fetch, "fetch", false, "fetch each source first, as source fetch does")
	cmd.Flags().BoolVar(&signal, "exit-code", false, "exit with 5 when some definitions compared aren't in step with NetBox")
	cmd.Flags().StringArrayVar(&sel.manufacturers, "manufacturer", nil,
		"compare only this manufacturer, by its exact name, and its device types; repeat it for more")
	cmd.Flags().StringArrayVar(&sel.models, "model", nil,
		"compare only the device types with this model, exactly, and their manufacturers; repeat it for more")
	cmd.Flags().BoolVar(&sel.all, "all", false, "compare every definition, not only those NetBox has")
	return cmd
}

// compareScope is what a run of compare compares
// (docs/adr/0030-compare-what-netbox-has-by-default.md). Its values are
// part of the CLI's JSON output.
type compareScope string

const (
	// scopeNetBox compares the definitions that match a NetBox object, or
	// collide with one: candidates and conflicts.
	scopeNetBox compareScope = "netbox"
	// scopeSelection compares what --manufacturer and --model name, absent
	// included.
	scopeSelection compareScope = "selection"
	// scopeAll compares every definition.
	scopeAll compareScope = "all"
)

// compareSelection is what the command line chose to compare.
type compareSelection struct {
	manufacturers, models []string
	all                   bool
}

func (sel compareSelection) scope() compareScope {
	switch {
	case sel.all:
		return scopeAll
	case len(sel.manufacturers) > 0 || len(sel.models) > 0:
		return scopeSelection
	}
	return scopeNetBox
}

// compareJSON is the result of `compare`, printed as text or JSON. Its JSON
// fields are part of the CLI's public interface (ADR 0018), and so are the
// objects', which the database records too.
type compareJSON struct {
	// OK says every source was read and its comparisons recorded, and
	// every file is valid.
	OK bool `json:"ok"`
	// Scope says what was compared.
	Scope      compareScope        `json:"scope"`
	NetBox     compareNetBoxJSON   `json:"netbox"`
	ComparedAt time.Time           `json:"compared_at"`
	Sources    []compareSourceJSON `json:"sources"`
	// Objects are the comparisons of the definitions, source by source, in
	// the order each source's format gives them, as JSON, which the
	// database records too.
	Objects  []json.RawMessage `json:"objects"`
	Warnings []warningJSON     `json:"warnings"`

	// results are the Objects, kept for text output only, with only the
	// rows that differ, since they take more memory than their JSON;
	// Objects are kept for JSON output only. unknown says which names given
	// to --manufacturer and --model no source read defines.
	results []compare.Result
	unknown []string
}

// counts returns how many definitions were compared, and how many of them
// are in step with NetBox, from the sources' counts.
func (r compareJSON) counts() (compared, inStep int) {
	for _, s := range r.Sources {
		compared += s.InStep + s.Differing + s.Absent + s.Conflicts
		inStep += s.InStep
	}
	return compared, inStep
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
	// how many of them were compared, which the scope chooses: by default,
	// the rest are those NetBox hasn't got. The others count the outcomes
	// of those compared.
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
		parts = append(parts, fmt.Sprintf("%d of the %d sources couldn't be read or recorded: %s", len(failed), len(r.Sources), strings.Join(failed, ", ")))
	}
	parts = append(parts, problemSummary(invalid, fileless)...)
	parts = append(parts, r.unknown...)
	return strings.Join(parts, "; ")
}

// sourceCompared is one source read for a comparison.
type sourceCompared struct {
	row *compareSourceJSON
	// status is that of the source's loads when its definitions were
	// read, rows the definitions as the database holds them, and unique
	// each of them cut down to its unique values, in the same order.
	status store.LoadStatus
	rows   []store.Definition
	unique []compare.Definition
	// chosen are the definitions compared, by their index in rows, and
	// whole each of them, read whole.
	chosen []int
	whole  []compare.Definition
}

// runCompare opens the database, connects to NetBox, fetches the sources if
// asked, compares them with NetBox, and records the comparisons, keeping
// the results for text output if forText, and their JSON otherwise. It
// closes the database before it returns, so that nothing is printed if
// closing it fails.
func (a *app) runCompare(ctx context.Context, cfg *config.Config, sources []config.Source, fetch bool, sel compareSelection, forText bool) (_ compareJSON, err error) {
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
		fetchErrors = fetchProblems(fetched)
	}
	return a.compareSources(ctx, cfg, db, c, sources, fetchErrors, sel, forText)
}

// compareSources reads each source's last fetched version, chooses the
// definitions to compare, compares them with NetBox, and records the
// comparisons. It reads each definition cut down to its unique values
// first, and whole only once it is chosen: by default, once its unique
// values match a NetBox object, or collide with one. A source that can't
// be read, or whose comparisons can't be recorded, is reported in the
// result; NetBox and the database failing are errors.
func (a *app) compareSources(ctx context.Context, cfg *config.Config, db store.Store, c *netbox.Client, sources []config.Source,
	fetchErrors map[string]string, sel compareSelection, forText bool,
) (compareJSON, error) {
	specs := dcim.Specs()
	scope := sel.scope()
	result := compareJSON{OK: true, Scope: scope, Sources: []compareSourceJSON{}, Objects: []json.RawMessage{}, Warnings: []warningJSON{}}
	var compared []sourceCompared
	for _, s := range sources {
		row := &compareSourceJSON{Name: s.Name, Format: s.Format, Problems: []problemJSON{}}
		sc := sourceCompared{row: row}
		if msg, ok := fetchErrors[s.Name]; ok {
			a.failSource(row, msg)
		} else if read, err := a.readSource(ctx, db, cfg, s, scope); err != nil {
			if ctx.Err() != nil {
				return compareJSON{}, fmt.Errorf("interrupted while reading source %s: %w", s.Name, ctx.Err())
			}
			if _, ok := errors.AsType[sourceProblem](err); !ok {
				return compareJSON{}, err
			}
			a.failSource(row, err.Error())
		} else {
			res := read.result
			row.Ref, row.Version = &read.ref, &read.version
			row.Files, row.InvalidFiles, row.Definitions = res.Files, res.Invalid, len(res.Definitions)
			row.OK = readCleanly(res)
			row.Problems = problemsOf(res.Problems)
			sc.status, sc.rows = read.status, read.rows
			// What the rows are kept for is their IDs and keys, and
			// what a selection finds them by.
			for i := range sc.rows {
				sc.rows[i].JSON, sc.rows[i].UniqueValues = nil, nil
			}
			sc.unique = make([]compare.Definition, len(res.Definitions))
			for i, d := range res.Definitions {
				sc.unique[i] = compare.Definition{Source: s.Name, Definition: d}
			}
			if scope == scopeAll {
				sc.whole = sc.unique
			}
		}
		compared = append(compared, sc)
	}

	// A name nothing defines, such as a typing mistake, leaves the
	// comparison short of what was asked for, so the run isn't OK.
	invalid := slices.ContainsFunc(compared, func(sc sourceCompared) bool { return sc.row.InvalidFiles > 0 })
	for _, msg := range sel.choose(specs, compared) {
		a.logger.Warn(msg)
		if invalid {
			msg += "; some files are invalid, and one of them may"
		}
		result.Warnings = append(result.Warnings, warningJSON{Message: msg})
		result.OK = false
		result.unknown = append(result.unknown, msg)
	}

	// NetBox's objects that stand on their own, which the definitions
	// chosen could match or collide with, read for their unique values.
	var first []compare.Definition
	for _, sc := range compared {
		for _, i := range sc.chosen {
			first = append(first, sc.unique[i])
		}
	}
	var observed []compare.Observed
	if len(first) > 0 {
		var err error
		if observed, err = readStandalone(ctx, c, specs, first); err != nil {
			return compareJSON{}, err
		}
	}
	// By default, only what NetBox has is compared: what the definitions'
	// unique values match or collide with, which the whole definitions do
	// too (compare.UniqueValues).
	if scope == scopeNetBox && len(first) > 0 {
		outcomes, err := compare.Compare(specs, first, observed)
		if err != nil {
			return compareJSON{}, fmt.Errorf("compare with NetBox: %w", err)
		}
		for j := range compared {
			sc := &compared[j]
			kept := sc.chosen[:0]
			for _, i := range sc.chosen {
				if outcomes[0].Outcome != compare.Absent {
					kept = append(kept, i)
				}
				outcomes = outcomes[1:]
			}
			sc.chosen = kept
		}
	}
	// The unique values have chosen what to read whole: let them go first.
	for j := range compared {
		compared[j].unique = nil
	}

	// The whole of each definition compared, which --all read already.
	for j := range compared {
		sc := &compared[j]
		if sc.row.Version == nil || scope == scopeAll {
			continue
		}
		if err := readChosen(ctx, db, specs, sc); err != nil {
			if ctx.Err() != nil {
				return compareJSON{}, fmt.Errorf("interrupted while reading source %s: %w", sc.row.Name, ctx.Err())
			}
			if _, ok := errors.AsType[sourceProblem](err); !ok {
				return compareJSON{}, err
			}
			a.failSource(sc.row, err.Error())
			sc.row.Version, sc.row.Ref, sc.chosen, sc.whole = nil, nil, nil, nil
		}
	}
	var defs []compare.Definition
	for _, sc := range compared {
		defs = append(defs, sc.whole...)
	}
	var results []compare.Result
	if len(defs) > 0 {
		children, childrenRead, err := readChildrenOf(ctx, c, specs, defs, observed)
		if err != nil {
			return compareJSON{}, err
		}
		if results, err = compare.Compare(specs, defs, append(observed, children...)); err != nil {
			return compareJSON{}, fmt.Errorf("compare with NetBox: %w", err)
		}
		// An object whose children weren't read would show every one of
		// them as missing.
		for _, r := range results {
			if s := model.SpecOf(specs, r.Type); s != nil && len(s.Children) > 0 && r.NetBoxID != 0 && !childrenRead[r.Type][r.NetBoxID] {
				return compareJSON{}, fmt.Errorf("compare with NetBox: the children of NetBox's %s #%d weren't read", typeName(r.Type), r.NetBoxID)
			}
		}
	}
	info := c.Info()
	result.NetBox = compareNetBoxJSON{URL: info.URL, Version: info.Version}
	// To the microsecond, as the database records it.
	result.ComparedAt = time.Now().UTC().Truncate(time.Microsecond)

	if err := checkResults(compared, results, scope); err != nil {
		return compareJSON{}, err
	}
	for _, sc := range compared {
		if sc.row.Version == nil {
			result.OK = false
			result.Sources = append(result.Sources, *sc.row)
			continue
		}
		sc.row.Compared = len(sc.chosen)
		mine := results[:len(sc.chosen)]
		results = results[len(sc.chosen):]
		// Each result is written as JSON once, for the database and for
		// the output alike. For JSON output, a result is let go once it is
		// written, since it takes more memory than its JSON, keeping only
		// what the counts and the database need.
		written := make([]json.RawMessage, len(mine))
		outcomes := make([]outcomeOf, len(mine))
		for i := range mine {
			out, err := json.Marshal(mine[i])
			if err != nil {
				return compareJSON{}, fmt.Errorf("source %s: write the comparison of %s: %w", sc.row.Name, sc.rows[sc.chosen[i]].DefinitionKey, err)
			}
			written[i] = out
			outcomes[i] = outcomeOf{outcome: mine[i].Outcome, netboxID: mine[i].NetBoxID, differences: mine[i].Differences, inStep: mine[i].InStep()}
			if forText {
				mine[i].Rows = differing(mine[i].Rows)
			} else {
				mine[i] = compare.Result{}
			}
		}
		if err := recordComparisons(ctx, db, sc, scope, outcomes, written, result); err != nil {
			if !errors.Is(err, store.ErrSnapshotChanged) {
				return compareJSON{}, err
			}
			// No quote before the error, which can hold JSON, so that the
			// message can't read as the error quoted (CodeQL's
			// go/unsafe-quoting).
			a.failSource(sc.row, fmt.Sprintf("its comparisons were not recorded: %v; compare again, or if the source was fetched meanwhile, run 'netbox-trueup-ai source fetch', or use --fetch, first", err))
		}
		for _, o := range outcomes {
			switch {
			case o.inStep:
				sc.row.InStep++
			case o.outcome == compare.Candidate:
				sc.row.Differing++
			case o.outcome == compare.Absent:
				sc.row.Absent++
			case o.outcome == compare.Conflict:
				sc.row.Conflicts++
			}
		}
		result.OK = result.OK && sc.row.OK
		result.Sources = append(result.Sources, *sc.row)
		if forText {
			result.results = append(result.results, mine...)
		} else {
			result.Objects = append(result.Objects, written...)
		}
	}
	return result, nil
}

// readSource reads what the database holds of a source's last fetched
// version: every definition whole for --all, which compares them all, or
// else cut down to their unique values, which choose what to read whole.
func (a *app) readSource(ctx context.Context, db store.Store, cfg *config.Config, s config.Source, scope compareScope) (sourceRead, error) {
	if scope == scopeAll {
		return a.readLoaded(ctx, db, cfg, s, "")
	}
	return a.readUniqueValues(ctx, db, cfg, s)
}

// choose chooses the definitions of each source to compare, by their index
// in its rows: every one, by default, for matching with NetBox first, and
// with --all; or what --manufacturer and --model name. It returns a
// warning for each name given that no source read defines.
//
// A selection compares the manufacturers that --manufacturer names, the
// device types of those manufacturers, or of any, with the models --model
// names, or with any, and the manufacturers of those device types, as an
// import of them needs, and as their comparisons say what their
// manufacturers' are. Names count as NetBox would store them: without
// white space at either end, as the specs normalize them.
func (sel compareSelection) choose(specs []*model.Spec, sources []sourceCompared) []string {
	if sel.scope() != scopeSelection {
		for j := range sources {
			sc := &sources[j]
			sc.chosen = make([]int, len(sc.rows))
			for i := range sc.rows {
				sc.chosen[i] = i
			}
		}
		return nil
	}
	name := normalizer(specs, "dcim.manufacturer", "name")
	modelName := normalizer(specs, "dcim.devicetype", "model")
	manufacturers, models := map[string]bool{}, map[string]bool{}
	for _, m := range sel.manufacturers {
		manufacturers[name(m)] = true
	}
	for _, m := range sel.models {
		models[modelName(m)] = true
	}
	named, found := map[string]bool{}, map[string]bool{}
	for j := range sources {
		sc := &sources[j]
		take := make([]bool, len(sc.rows))
		makers := map[string]bool{}
		for i, d := range sc.rows {
			maker := name(d.Manufacturer)
			if d.Manufacturer != "" {
				named[maker] = true
			}
			if d.ObjectType != "dcim.devicetype" || len(manufacturers) > 0 && !manufacturers[maker] {
				continue
			}
			m := modelName(d.Model)
			if len(models) > 0 && !models[m] {
				continue
			}
			take[i], makers[maker], found[m] = true, true, true
		}
		for i, d := range sc.rows {
			if d.ObjectType == "dcim.manufacturer" && (manufacturers[name(d.Manufacturer)] || makers[name(d.Manufacturer)]) {
				take[i] = true
			}
		}
		for i := range take {
			if take[i] {
				sc.chosen = append(sc.chosen, i)
			}
		}
	}
	var unknown []string
	for _, m := range slices.Sorted(maps.Keys(manufacturers)) {
		if !named[m] {
			unknown = append(unknown, "no source read defines the manufacturer "+strconv.Quote(m))
		}
	}
	var of string
	switch quoted := quoteAll(slices.Sorted(maps.Keys(manufacturers))); {
	case len(quoted) == 1:
		of = " of the manufacturer " + quoted[0]
	case len(quoted) > 1:
		of = " of any of the manufacturers " + strings.Join(quoted, ", ")
	}
	for _, m := range slices.Sorted(maps.Keys(models)) {
		if !found[m] {
			unknown = append(unknown, "no source read defines a device type with the model "+strconv.Quote(m)+of)
		}
	}
	return unknown
}

// normalizer returns a function that gives text as NetBox would store it in
// a field of a type, as its spec normalizes it.
func normalizer(specs []*model.Spec, typ, field string) func(string) string {
	var f *model.Field
	if s := model.SpecOf(specs, typ); s != nil {
		f = s.Field(field)
	}
	return func(text string) string {
		if f == nil {
			return text
		}
		v, _ := f.Normalize(model.String(text)).(model.String)
		return string(v)
	}
}

func quoteAll(names []string) []string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = strconv.Quote(n)
	}
	return quoted
}

// readChosen reads the definitions chosen of a source whole. They must be
// those of the load whose unique values were read: if the source was
// loaded again meanwhile, such as by a fetch from another process, it
// returns a sourceProblem.
func readChosen(ctx context.Context, db store.Store, specs []*model.Spec, sc *sourceCompared) error {
	if len(sc.chosen) == 0 {
		return nil
	}
	ids := make([]int64, len(sc.chosen))
	for k, i := range sc.chosen {
		ids[k] = sc.rows[i].ID
	}
	read, err := db.DefinitionsByID(ctx, sc.row.Name, ids)
	if errors.Is(err, store.ErrNotFound) {
		return sourceProblem{errors.New("the source was removed while it was compared, by a fetch whose configuration lacks it")}
	}
	if err != nil {
		return err
	}
	if read.Loaded != sc.status.Loaded || !read.LoadedAt.Equal(sc.status.LoadedAt) {
		return sourceProblem{errors.New("the source was fetched or loaded again while it was compared: compare again")}
	}
	byID := make(map[int64]store.Definition, len(read.Definitions))
	for _, d := range read.Definitions {
		byID[d.ID] = d
	}
	sc.whole = make([]compare.Definition, len(sc.chosen))
	for k, i := range sc.chosen {
		d, ok := byID[sc.rows[i].ID]
		if !ok || d.DefinitionKey != sc.rows[i].DefinitionKey {
			return fmt.Errorf("source %s: the database doesn't give definition #%d, %s, of the load it holds", sc.row.Name, sc.rows[i].ID, sc.rows[i].DefinitionKey)
		}
		def, err := format.UnmarshalDefinition(specs, d.JSON)
		if err != nil {
			return sourceProblem{fmt.Errorf("TrueUp can't read back %s, though it read its unique values; please report it: %w", d.DefinitionKey, err)}
		}
		sc.whole[k] = compare.Definition{Source: sc.row.Name, Definition: def}
	}
	return nil
}

// checkResults checks that Compare gave a result for each definition
// compared of the sources read, in their order, with an outcome it knows,
// and by default, none that NetBox hasn't got, since its unique values
// matched NetBox; before any source's comparisons are recorded.
func checkResults(compared []sourceCompared, results []compare.Result, scope compareScope) error {
	for _, sc := range compared {
		if sc.row.Version == nil {
			continue
		}
		if len(results) < len(sc.whole) {
			return fmt.Errorf("compare with NetBox: %d results for the %d definitions of source %s", len(results), len(sc.whole), sc.row.Name)
		}
		for i, d := range sc.whole {
			r := results[i]
			if r.Type != d.Object.Type || len(r.Columns) == 0 || r.Columns[0].Source != d.Source || r.Columns[0].Path != d.Path {
				return fmt.Errorf("compare with NetBox: result %d of source %s is of another definition", i, sc.row.Name)
			}
			key := sc.rows[sc.chosen[i]].DefinitionKey
			switch r.Outcome {
			case compare.Candidate, compare.Conflict:
			case compare.Absent:
				if scope == scopeNetBox {
					return fmt.Errorf("compare with NetBox: %s is absent, though its unique values matched NetBox; please report it", key)
				}
			default:
				return fmt.Errorf("compare with NetBox: the comparison of %s has the outcome %q", key, r.Outcome)
			}
		}
		results = results[len(sc.whole):]
	}
	if len(results) > 0 {
		return fmt.Errorf("compare with NetBox: %d results of no definition", len(results))
	}
	return nil
}

func (a *app) failSource(row *compareSourceJSON, msg string) {
	row.OK, row.Error = false, &msg
	a.logger.Warn("can't compare source", "source", row.Name, "error", msg)
}

// differing returns the rows that differ, in a slice of their own, so that
// the others can be let go: all that text output shows.
func differing(rows []compare.Row) []compare.Row {
	var out []compare.Row
	for _, row := range rows {
		if row.Differs {
			out = append(out, row)
		}
	}
	return out
}

// outcomeOf is what the counts and the database need of a result.
type outcomeOf struct {
	outcome     compare.Outcome
	netboxID    int64
	differences int
	inStep      bool
}

// recordComparisons records one source's comparisons, results written as
// JSON, as the latest of their definitions, against the load whose
// definitions were read. Every scope but a selection looked at every
// definition, so its run is whole: the source's other comparisons are
// removed.
func recordComparisons(ctx context.Context, db store.Store, sc sourceCompared, scope compareScope, outcomes []outcomeOf, written []json.RawMessage, run compareJSON) error {
	rec := store.ComparisonRun{
		Source:        sc.row.Name,
		Loaded:        sc.status.Loaded,
		LoadedAt:      sc.status.LoadedAt,
		ComparedAt:    run.ComparedAt,
		NetBoxURL:     run.NetBox.URL,
		NetBoxVersion: run.NetBox.Version,
		Whole:         scope != scopeSelection,
	}
	for i, o := range outcomes {
		rec.Comparisons = append(rec.Comparisons, store.Comparison{
			DefinitionID: sc.rows[sc.chosen[i]].ID, Outcome: string(o.outcome), NetBoxID: o.netboxID, Differences: o.differences, Result: written[i],
		})
	}
	return db.RecordComparisons(ctx, rec)
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

// readStandalone reads the NetBox objects that stand on their own, which the
// definitions could match or collide with: every manufacturer, since a
// manufacturer's name and slug are each unique among all of them, and the
// device types of the manufacturers that the definitions name and NetBox
// has, since both of a device type's uniqueness rules include its
// manufacturer. The definitions may be cut down to their unique values.
// Each object is read through its spec, and one that can't be is an error,
// since leaving it out would make what it is show as absent.
func readStandalone(ctx context.Context, c *netbox.Client, specs []*model.Spec, defs []compare.Definition) ([]compare.Observed, error) {
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
			return nil, readListingError(deviceType, manufacturer, err)
		}
	}
	return observed, nil
}

// readChildrenOf reads the component templates of each device type, among
// the objects observed, that the definitions match, as the comparison will
// match them; of no others, which nothing compares. It returns them, and
// which device types' templates were read.
func readChildrenOf(ctx context.Context, c *netbox.Client, specs []*model.Spec, defs []compare.Definition, observed []compare.Observed) ([]compare.Observed, map[string]map[int64]bool, error) {
	deviceType := model.SpecOf(specs, "dcim.devicetype")
	if deviceType == nil {
		return nil, nil, errors.New("TrueUp has no spec of device types")
	}
	matched, err := compare.Matched(specs, defs, observed, deviceType.Type)
	if err != nil {
		return nil, nil, fmt.Errorf("compare with NetBox: %w", err)
	}
	childrenRead := map[string]map[int64]bool{deviceType.Type: {}}
	for _, id := range matched {
		childrenRead[deviceType.Type][id] = true
	}
	if len(matched) == 0 {
		return nil, childrenRead, nil
	}
	children, err := readChildren(ctx, c, specs, deviceType, matched)
	if err != nil {
		return nil, nil, err
	}
	return children, childrenRead, nil
}

// readChildren reads the children of the objects of type parent with IDs
// ids, a collection at a time, all at once, as each listing waits on its own
// requests; the client's cap on requests in flight still holds. The first
// listing that fails stops the others, and is the error returned. The
// children come in the collections' order.
func readChildren(ctx context.Context, c *netbox.Client, specs []*model.Spec, parent *model.Spec, ids []int64) ([]compare.Observed, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Every collection's spec, before any listing starts.
	of := make([]*model.Spec, len(parent.Children))
	for i, col := range parent.Children {
		s := model.SpecOf(specs, col.Type)
		if s == nil || s.Endpoint == "" {
			return nil, fmt.Errorf("TrueUp can't read the %s of %ss from NetBox", col.Name, typeName(parent.Type))
		}
		of[i] = s
	}
	read := make([][]compare.Observed, len(parent.Children))
	var (
		mu     sync.Mutex
		failed error
		wg     sync.WaitGroup
	)
	for i, col := range parent.Children {
		s := of[i]
		wg.Go(func() {
			err := c.ListByParent(ctx, s.Endpoint, col.Parent, ids, fieldsQuery(s), func(raw json.RawMessage) error {
				id, obj, err := s.DecodeAPI(specs, raw)
				if err != nil {
					return fmt.Errorf("NetBox's answer can't be read: %w", err)
				}
				read[i] = append(read[i], compare.Observed{ID: id, Object: obj})
				return nil
			})
			if err != nil {
				mu.Lock()
				defer mu.Unlock()
				if failed == nil {
					failed = readListingError(s, parent, err)
					cancel()
				}
			}
		})
	}
	wg.Wait()
	if failed != nil {
		return nil, failed
	}
	return slices.Concat(read...), nil
}

// fieldsQuery asks NetBox for an object's ID, the fields its spec
// describes and its nested collections, such as a front port template's
// mappings, and nothing else. NetBox leaves out a name it doesn't know,
// which DecodeAPI then reports, and gives a nested collection's children
// whole, which DecodeAPI reads as the children's spec says.
func fieldsQuery(s *model.Spec) url.Values {
	names := []string{"id"}
	for _, f := range s.Fields {
		if f.Ref != nil && f.Ref.Shape == model.ShapeTypeAndID {
			names = append(names, f.Ref.TypeField, f.Ref.IDField)
			continue
		}
		names = append(names, f.Name)
	}
	for _, col := range s.Children {
		if col.Nested {
			names = append(names, col.Name)
		}
	}
	return url.Values{"fields": {strings.Join(names, ",")}}
}

// readListingError is readError for a listing of the objects of type s
// that belong to some objects of type parent. NetBox refuses such a listing
// when one of those parents no longer exists (a ModelMultipleChoiceFilter,
// such as DeviceTypeComponentFilterSet.device_type_id in
// dcim/filtersets.py), as when it is deleted while TrueUp reads NetBox, so
// a refusal says to compare again.
func readListingError(s, parent *model.Spec, err error) error {
	if se, ok := errors.AsType[*netbox.StatusError](err); ok && se.StatusCode == http.StatusBadRequest && se.FromNetBox() {
		return fmt.Errorf("read %s from NetBox: %w; if a %s was deleted while TrueUp read NetBox, compare again", s.Type, err, typeName(parent.Type))
	}
	return readError(s, err)
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
	if line := notComparedText(r); line != "" {
		b.WriteString("\n" + line + "\n")
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
		twice := map[string][]string{}
		var where []string
		for _, c := range o.Collisions {
			if len(c.Path) > 0 {
				p := c.Path.String()
				if twice[p] == nil {
					where = append(where, p)
				}
				// A nested child, such as a front port's mapping, has no
				// NetBox ID.
				id := ""
				if c.ID != 0 {
					id = "#" + strconv.FormatInt(c.ID, 10)
				}
				twice[p] = append(twice[p], id)
				continue
			}
			verb := "matches"
			if len(c.Rule) > 1 {
				verb = "match"
			}
			parts = append(parts, fmt.Sprintf("its %s %s NetBox's %s #%d, %s",
				strings.Join(c.Rule, " and "), verb, typeName(o.Type), c.ID, identityText(o.Type, c.Identity)))
		}
		for _, p := range where {
			times, none := "twice", "neither"
			if n := len(twice[p]); n > 2 {
				times, none = strconv.Itoa(n)+" times", "none"
			}
			as := ""
			if ids := slices.DeleteFunc(twice[p], func(id string) bool { return id == "" }); len(ids) > 0 {
				as = ", as " + strings.Join(ids, ", ")
			}
			parts = append(parts, fmt.Sprintf("NetBox's #%d has %s %s%s, which NetBox should keep unique, so %s is compared",
				o.NetBoxID, p, times, as, none))
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
				mine, theirs := valueText(row.Cells[0]), valueText(row.Cells[1])
				if isChildRow(row) {
					mine, theirs = presenceText(row.Cells[0]), presenceText(row.Cells[1])
				}
				_, _ = fmt.Fprintf(tw, "    %s\t%s\t%s\n", cell(row.Path.String()), cell(mine), cell(theirs))
			}
		}
		if err := tw.Flush(); err != nil {
			return fmt.Errorf("write output: %w", err)
		}
	}

	var onlyNetBox []string
	for _, o := range r.results {
		var children []string
		for _, row := range o.Rows {
			if isChildRow(row) && row.Differs && !row.Managed {
				c := row.Path.String()
				if row.NetBoxID != 0 {
					c += " (#" + strconv.FormatInt(row.NetBoxID, 10) + ")"
				}
				children = append(children, c)
			}
		}
		if len(children) > 0 {
			onlyNetBox = append(onlyNetBox, fmt.Sprintf("  %s, NetBox #%d: %s", cell(objectText(o)), o.NetBoxID, cell(strings.Join(children, ", "))))
		}
	}
	if len(onlyNetBox) > 0 {
		b.WriteString("\nOnly in NetBox, which TrueUp leaves as they are:\n" + strings.Join(onlyNetBox, "\n") + "\n")
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

// notComparedText says how many definitions NetBox hasn't got weren't
// compared, by default, or is "".
func notComparedText(r compareJSON) string {
	if r.Scope != scopeNetBox {
		return ""
	}
	n := 0
	for _, s := range r.Sources {
		if s.Version != nil {
			n += s.Definitions - s.Compared
		}
	}
	switch n {
	case 0:
		return ""
	case 1:
		return "1 definition NetBox hasn't got wasn't compared: --all compares every definition, and --manufacturer or --model those named."
	}
	return fmt.Sprintf("%d definitions NetBox hasn't got weren't compared: --all compares every definition, and --manufacturer or --model those named.", n)
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
		if len(v.Key) == 0 {
			// By its ID alone, such as a template's reference to one that
			// isn't another of its device type's.
			return fmt.Sprintf("#%d", v.ID)
		}
		name := strconv.Quote(text(v))
		if v.ID != 0 {
			return fmt.Sprintf("%s (#%d)", name, v.ID)
		}
		return name
	}
	return v.String()
}

// isChildRow reports whether a row says which versions have a child, as its
// path ends at the child's key, rather than holding a field.
func isChildRow(row compare.Row) bool {
	return len(row.Path) > 0 && row.Path[len(row.Path)-1].Key != nil
}

// presenceText writes a child row's cell: whether that version has the
// child.
func presenceText(v model.Value) string {
	if b, ok := v.(model.Boolean); ok && bool(b) {
		return "present"
	}
	return "missing"
}

// writeCompareJSON writes the result as writeJSON does, a field and an
// object at a time, so that the document, which runs to hundreds of
// megabytes for the whole of devicetype-library, is never held in memory
// whole on top of the objects it is made of. It names compareJSON's fields
// itself: TestCompareJSONIsWrittenAsWriteJSONWould fails when they differ
// from what writeJSON writes, as a new field would.
func writeCompareJSON(w io.Writer, r compareJSON) error {
	bw := bufio.NewWriterSize(w, 1<<16)
	var indented bytes.Buffer
	// value writes v's JSON as writeJSON would, at an indent of prefix.
	value := func(v any, prefix string) error {
		raw, ok := v.(json.RawMessage)
		if !ok {
			var b bytes.Buffer
			enc := json.NewEncoder(&b)
			enc.SetEscapeHTML(false)
			if err := enc.Encode(v); err != nil {
				return err
			}
			raw = bytes.TrimSuffix(b.Bytes(), []byte("\n"))
		}
		indented.Reset()
		if err := json.Indent(&indented, raw, prefix, "  "); err != nil {
			return err
		}
		_, err := bw.Write(indented.Bytes())
		return err
	}
	text := func(s string) error {
		_, err := bw.WriteString(s)
		return err
	}
	err := func() error {
		if err := text("{\n"); err != nil {
			return err
		}
		for _, f := range []struct {
			name  string
			value any
		}{{"ok", r.OK}, {"scope", r.Scope}, {"netbox", r.NetBox}, {"compared_at", r.ComparedAt}, {"sources", r.Sources}} {
			if err := text("  " + strconv.Quote(f.name) + ": "); err != nil {
				return err
			}
			if err := value(f.value, "  "); err != nil {
				return err
			}
			if err := text(",\n"); err != nil {
				return err
			}
		}
		switch {
		case r.Objects == nil:
			if err := text("  \"objects\": null,\n"); err != nil {
				return err
			}
		case len(r.Objects) == 0:
			if err := text("  \"objects\": [],\n"); err != nil {
				return err
			}
		default:
			if err := text("  \"objects\": [\n"); err != nil {
				return err
			}
			for i, o := range r.Objects {
				if err := text("    "); err != nil {
					return err
				}
				if err := value(o, "    "); err != nil {
					return err
				}
				sep := ",\n"
				if i == len(r.Objects)-1 {
					sep = "\n"
				}
				if err := text(sep); err != nil {
					return err
				}
			}
			if err := text("  ],\n"); err != nil {
				return err
			}
		}
		if err := text("  \"warnings\": "); err != nil {
			return err
		}
		if err := value(r.Warnings, "  "); err != nil {
			return err
		}
		if err := text("\n}\n"); err != nil {
			return err
		}
		return bw.Flush()
	}()
	if err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}
