package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/format"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/glob"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model/dcim"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/source"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/version"
)

// Loading a source reads its last fetched version with its format, once,
// and records the definitions and problems it found in the database, which
// list and compare then read instead of the source's files
// (docs/adr/0029-keep-a-lookup-table-of-definitions.md). source fetch loads
// each version it fetches; list and compare load one the database holds
// nothing of, or what another version of TrueUp's format, or other
// settings of the source, read.

// sourceProblem is a problem with one source, such as one never fetched,
// or files its format can't read, which a command reports with that source
// and goes on, rather than a failure of the command, such as the database's.
type sourceProblem struct{ error }

func (p sourceProblem) Unwrap() error { return p.error }

// cacheProblem is a problem with this host's cache of a source, such as one
// gone, holding another version than the database records, or failing to
// give back a file it listed, rather than with the source's content: a load it stops isn't recorded, since another
// host sharing the database, or this one a moment later, may read it.
type cacheProblem struct{ error }

func (p cacheProblem) Unwrap() error { return p.error }

// sourceRead is what a source's format read from its last fetched version,
// as the database holds it.
type sourceRead struct {
	ref, version string
	result       *format.Result
	// keys are the keys the database holds the definitions by, and json
	// the definitions as it holds them, which list --output json prints.
	keys []store.DefinitionKey
	json []json.RawMessage
}

// loadKey is what decides what a load of a source's version reads: the
// format and its version, read by this release of TrueUp, and the source's
// settings that choose the files read and say where they come from. A load
// by another key is read again.
type loadKey struct {
	reader, settings string
}

// loadKeyOf returns the load key of a source as configured: its format's
// reader and TrueUp's release, such as "devicetype-library/1 (TrueUp
// 1.2.0)", so that each release reads sources again, whatever it changed in
// how they are read, as the format's version alone would need raising for;
// and a SHA-256 of the source's kind, url, ref, format, include and exclude
// patterns, and the limits that leave files out. Patterns are sets, so
// their order, and a pattern given twice, don't change it.
func loadKeyOf(s config.Source) (loadKey, error) {
	f, ok := formats[s.Format]
	if !ok {
		return loadKey{}, fmt.Errorf("TrueUp can't read sources of format %q", s.Format)
	}
	set := func(patterns []string) []string {
		return slices.Compact(slices.Sorted(slices.Values(patterns)))
	}
	data, err := json.Marshal(struct {
		Kind, URL, Ref, Format string
		Include, Exclude       []string
		MaxFileSize            int64
		MaxFiles               int
	}{s.Kind, s.URL, s.Ref, s.Format, set(s.Include), set(s.Exclude), s.Limits.MaxFileSize, s.Limits.MaxFiles})
	if err != nil {
		return loadKey{}, fmt.Errorf("write the settings of source %s: %w", s.Name, err)
	}
	sum := sha256.Sum256(data)
	reader := format.Reader(f) + " (TrueUp " + version.String() + ")"
	return loadKey{reader: reader, settings: "sha256:" + hex.EncodeToString(sum[:])}, nil
}

// fetchHint says how to fetch a source.
const fetchHint = "run 'netbox-trueup-ai source fetch', or use --fetch"

// notFetched is the problem with a source the database has no snapshot of.
var notFetched = sourceProblem{fmt.Errorf("%w: %s", source.ErrNotFetched, fetchHint)}

// readLoaded returns the definitions that the database holds of a source's
// last fetched version, those whose manufacturer, model or part number
// holds search, if it isn't "", with all the source's problems. If the
// database holds nothing that this release read from that version with the
// source's settings as they are, the source is loaded first, unless such a
// load failed already, when the problem it found is returned, until source
// fetch tries again; and if it holds
// definitions this release can't read back, which a format whose version
// wasn't raised would leave, it is loaded again. A problem with the source
// is returned as a sourceProblem.
func (a *app) readLoaded(ctx context.Context, db store.Store, cfg *config.Config, s config.Source, search string) (sourceRead, error) {
	key, err := loadKeyOf(s)
	if err != nil {
		return sourceRead{}, sourceProblem{err}
	}
	reload := false
	// A fetch by another process between loading and reading leaves the
	// definitions of an older version, which another round loads again. A
	// round that finds definitions it can't read back is always followed by
	// one that loads them again.
	for round := 0; round < 3 || reload; round++ {
		status, err := db.LoadStatus(ctx, s.Name)
		if errors.Is(err, store.ErrNotFound) {
			return sourceRead{}, notFetched
		}
		if err != nil {
			return sourceRead{}, err
		}
		if !reload && !status.Current(key.reader, key.settings) && status.Failed(key.reader, key.settings) {
			return sourceRead{}, failedAlready(status)
		}
		if reload || !status.Current(key.reader, key.settings) {
			err := a.loadSource(ctx, db, cfg, s, reload)
			if errors.Is(err, store.ErrSnapshotChanged) {
				// Another host sharing the database fetched the source
				// while this one loaded it: the next round reads what
				// that fetch loaded.
				reload = false
				continue
			}
			if err != nil {
				return sourceRead{}, err
			}
		}
		loaded, err := db.Definitions(ctx, s.Name, search)
		if errors.Is(err, store.ErrNotFound) {
			// Removed meanwhile, by a fetch whose configuration lacks it.
			return sourceRead{}, notFetched
		}
		if err != nil {
			return sourceRead{}, err
		}
		if !loaded.Current(key.reader, key.settings) {
			reload = false
			continue
		}
		read, err := decodeLoaded(loaded)
		if err == nil {
			return read, nil
		}
		if reload {
			return sourceRead{}, sourceProblem{fmt.Errorf("TrueUp can't read back the definitions it just loaded; please report it: %w", err)}
		}
		a.logger.Warn("the database holds definitions of the source that this release can't read back, so it loads the source again",
			"source", s.Name, "error", err)
		reload = true
	}
	return sourceRead{}, sourceProblem{errors.New("another process fetched the source, or loaded it with another release of TrueUp or other settings of the source, each time it was read: try again")}
}

// failedAlready is the problem a source's latest load found, when a load now
// would read what it read: source fetch, or other settings of the source,
// try again.
func failedAlready(status store.LoadStatus) sourceProblem {
	return sourceProblem{errors.New(status.Error)}
}

// decodeLoaded reads back what the database holds of a source.
func decodeLoaded(defs store.SourceDefinitions) (sourceRead, error) {
	specs := dcim.Specs()
	res := &format.Result{
		Definitions: make([]format.Definition, len(defs.Definitions)),
		Problems:    make([]format.Problem, len(defs.Problems)),
		Files:       defs.Files,
		Invalid:     defs.InvalidFiles,
	}
	read := sourceRead{
		ref: defs.SnapshotRef, version: defs.Loaded.Version, result: res,
		keys: make([]store.DefinitionKey, len(defs.Definitions)),
		json: make([]json.RawMessage, len(defs.Definitions)),
	}
	for i, d := range defs.Definitions {
		def, err := format.UnmarshalDefinition(specs, d.JSON)
		if err != nil {
			return sourceRead{}, fmt.Errorf("%s: %w", d.DefinitionKey, err)
		}
		res.Definitions[i], read.keys[i], read.json[i] = def, d.DefinitionKey, d.JSON
	}
	for i, p := range defs.Problems {
		res.Problems[i] = format.Problem{
			Severity: format.Severity(p.Severity), Path: p.Path, Line: p.Line, Column: p.Column, Field: p.Field, Reason: p.Reason,
		}
	}
	return read, nil
}

// loadSource loads a source's last fetched version into the database, as
// loadUnlessCurrent does, holding the source's fetch lock, so that fetches
// and loads of the source take turns. force also loads a version whose
// load failed already.
func (a *app) loadSource(ctx context.Context, db store.Store, cfg *config.Config, s config.Source, force bool) error {
	root, err := cacheRoot(cfg)
	if err != nil {
		// Such as a data directory this host can't write: the sources the
		// database holds current definitions of are still read.
		return sourceProblem{err}
	}
	unlock, err := source.LockCache(ctx, root, s.Name, s.Limits.FetchTimeout, func() {
		a.logger.Info("waiting for another TrueUp process to finish fetching or loading the source", "source", s.Name)
	})
	switch {
	case err == nil:
	case ctx.Err() != nil:
		return fmt.Errorf("interrupted while waiting to load source %s: %w", s.Name, ctx.Err())
	default:
		// Another process fetching it, past the source's fetch timeout,
		// or a lock this host can't take, such as a lock file it can't
		// open: a problem with this source's cache, which nothing records.
		return sourceProblem{err}
	}
	defer func() {
		if err := unlock(); err != nil {
			a.logger.Warn("can't release the source's fetch lock", "source", s.Name, "error", err)
		}
	}()
	_, err = a.loadUnlessCurrent(ctx, db, a.sourceKinds(root), s, force, force)
	return err
}

// loadUnlessCurrent loads the source's recorded snapshot into the database,
// as loadLocked does, unless force is false and the database holds what
// this release read from it with the source's settings as they are, as
// when another process loaded it meanwhile; and, unless retry is true,
// returns the problem the latest load found if it read the same. The
// caller holds the source's fetch lock. It returns the status of the
// source's loads afterwards.
func (a *app) loadUnlessCurrent(ctx context.Context, db store.Store, kinds map[string]source.Kind, s config.Source, force, retry bool) (store.LoadStatus, error) {
	key, err := loadKeyOf(s)
	if err != nil {
		return store.LoadStatus{}, sourceProblem{err}
	}
	status, err := db.LoadStatus(ctx, s.Name)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return store.LoadStatus{}, notFetched
	case err != nil && ctx.Err() != nil:
		return store.LoadStatus{}, fmt.Errorf("interrupted while loading source %s: %w", s.Name, ctx.Err())
	case err != nil:
		return store.LoadStatus{}, err
	}
	if !force && status.Current(key.reader, key.settings) {
		return status, nil
	}
	if !retry && status.Failed(key.reader, key.settings) {
		return store.LoadStatus{}, failedAlready(status)
	}
	load, err := a.loadLocked(ctx, db, kinds, s, status.SnapshotVersion, key)
	if err != nil {
		return store.LoadStatus{}, err
	}
	loaded := store.LoadKey{Version: load.Version, Reader: load.Reader, Settings: load.Settings}
	status.Latest, status.Loaded, status.Error, status.StartedAt, status.LoadedAt = loaded, loaded, "", load.StartedAt, load.StartedAt
	status.Files, status.InvalidFiles, status.Definitions = load.Files, load.InvalidFiles, len(load.Definitions)
	return status, nil
}

// loadLocked reads a source's cached snapshot of version, which must be
// the one the database records, with its format, and records the
// definitions and problems it found, or why it couldn't read them, in the
// database. The caller holds the source's fetch lock. A load that fails for
// what the source holds is recorded, and returned as a sourceProblem,
// which the caller reports; so is one the database can't record because the
// source's snapshot isn't the version read. A load that fails for this
// host's cache, or is interrupted, isn't recorded. Any other problem with
// the database is an error.
func (a *app) loadLocked(ctx context.Context, db store.Store, kinds map[string]source.Kind, s config.Source, version string, key loadKey) (store.Load, error) {
	interrupted := func() (store.Load, error) {
		return store.Load{}, fmt.Errorf("interrupted while loading source %s: %w", s.Name, ctx.Err())
	}
	// What any load of the source records, which failed or not.
	base := store.Load{Source: s.Name, Version: version, Reader: key.reader, Settings: key.settings, StartedAt: time.Now()}
	load := base
	if readErr := a.readForLoad(ctx, kinds, s, &load); readErr != nil {
		if ctx.Err() != nil {
			return interrupted()
		}
		if _, ok := errors.AsType[cacheProblem](readErr); ok {
			return store.Load{}, sourceProblem{readErr}
		}
		failed := base
		failed.Error = readErr.Error()
		if err := recordLoad(ctx, db, failed); err != nil {
			if p, ok := errors.AsType[sourceProblem](err); ok {
				return store.Load{}, sourceProblem{fmt.Errorf("%w; and recording that failed: %w", readErr, p.error)}
			}
			if ctx.Err() != nil {
				return interrupted()
			}
			return store.Load{}, fmt.Errorf("%w, recording that the source's files couldn't be read: %w", err, readErr)
		}
		return store.Load{}, sourceProblem{readErr}
	}
	if err := recordLoad(ctx, db, load); err != nil {
		if ctx.Err() != nil {
			return interrupted()
		}
		return store.Load{}, err
	}
	a.logger.Info("loaded the source's definitions into the database", "source", s.Name, "version", version,
		"definitions", len(load.Definitions), "duration", time.Since(load.StartedAt).Round(time.Millisecond))
	return load, nil
}

// recordLoad records a load, and returns the database's finding that the
// source's snapshot isn't the version read as a sourceProblem. The caller
// holds the source's fetch lock, so the database and the cache disagree,
// such as after the database was replaced, or another host sharing it
// fetched the source.
func recordLoad(ctx context.Context, db store.Store, load store.Load) error {
	err := db.RecordLoad(ctx, load)
	if errors.Is(err, store.ErrSnapshotChanged) {
		return sourceProblem{fmt.Errorf("%w: run 'netbox-trueup-ai source fetch'", err)}
	}
	return err
}

// readForLoad reads the source's cached snapshot of load.Version with its
// format, and fills in what the load records: each definition, as it reads
// back from its JSON, with what it is searched and shown by and its file's
// digest, and the problems.
func (a *app) readForLoad(ctx context.Context, kinds map[string]source.Kind, s config.Source, load *store.Load) error {
	kind, ok := kinds[s.Kind]
	if !ok {
		return fmt.Errorf("TrueUp can't read sources of kind %q", s.Kind)
	}
	spec, f, err := sourceSpec(s)
	if err != nil {
		return err
	}
	// Walk, and hash, only the files the format reads, not the library's
	// images.
	if spec.Only, err = glob.CompileAll(f.Reads()); err != nil {
		return fmt.Errorf("format %s: %w", f.Name(), err)
	}
	snap, err := source.Open(ctx, kind, spec)
	if errors.Is(err, source.ErrNotFetched) {
		// The kind says why, such as that the source's url changed since.
		return cacheProblem{fmt.Errorf("%w: %s", err, fetchHint)}
	}
	if err != nil {
		return cacheProblem{fmt.Errorf("open the cached version: %w", err)}
	}
	defer func() {
		if err := snap.Close(); err != nil {
			a.logger.Warn("can't close the source's cache", "source", s.Name, "error", err)
		}
	}()
	if snap.Version != load.Version {
		return cacheProblem{fmt.Errorf("the cache holds version %s, but the database records %s: run 'netbox-trueup-ai source fetch'", snap.Version, load.Version)}
	}
	res, err := f.Read(ctx, snap)
	if _, ok := errors.AsType[*source.ReadError](err); ok {
		// This host's cache failed to give back a file it listed, which
		// says nothing of the version's files.
		return cacheProblem{fmt.Errorf("format %s: %w", f.Name(), err)}
	}
	if err != nil {
		return fmt.Errorf("format %s: %w", f.Name(), err)
	}
	digests := make(map[string]string, len(snap.Files))
	for _, file := range snap.Files {
		digests[file.Path] = file.Digest
	}
	fillLoad(load, res, digests)
	return nil
}

// fillLoad fills in what a load records of what a format read: each
// definition, as the database keeps it (storedDefinition), and the
// problems, with a definition that can't be kept as one more.
func fillLoad(load *store.Load, res *format.Result, digests map[string]string) {
	specs := dcim.Specs()
	load.Files, load.InvalidFiles = res.Files, res.Invalid
	problems := slices.Clone(res.Problems)
	load.Definitions = make([]store.Definition, 0, len(res.Definitions))
	// The files of definitions that can't be kept, which become invalid;
	// the format gives none for a file it refused.
	skipped := map[string]bool{}
	for _, d := range res.Definitions {
		def, err := storedDefinition(specs, d, digests)
		if err != nil {
			// A definition TrueUp can't keep exactly as it was read, which
			// is TrueUp's fault, not the file's, makes that file invalid,
			// once however many it defines, and leaves the others.
			problems = append(problems, format.Problem{
				Severity: format.Error, Path: d.Path,
				Reason: "TrueUp can't keep this definition as it was read, so it is skipped; please report it: " + err.Error(),
			})
			// A derived one has no file of its own: its problem, an error,
			// is what makes the source not OK.
			if d.Path != "" && !skipped[d.Path] {
				skipped[d.Path] = true
				load.InvalidFiles++
			}
			continue
		}
		load.Definitions = append(load.Definitions, def)
	}
	// In the format's order: problems that concern no one file first, then
	// by path, each file's in the order found.
	slices.SortStableFunc(problems, func(a, b format.Problem) int { return strings.Compare(a.Path, b.Path) })
	load.Problems = make([]store.Problem, len(problems))
	for i, p := range problems {
		load.Problems[i] = store.Problem{
			Severity: string(p.Severity), Path: p.Path, Line: p.Line, Column: p.Column, Field: p.Field, Reason: p.Reason,
		}
	}
}

// storedDefinition returns a definition as the database keeps it: by its
// key, as it reads back from its JSON, with what it is searched and shown
// by and its file's digest.
func storedDefinition(specs []*model.Spec, d format.Definition, digests map[string]string) (store.Definition, error) {
	key, err := definitionKey(d)
	if err != nil {
		return store.Definition{}, err
	}
	data, err := format.EncodeDefinition(specs, d)
	if err != nil {
		return store.Definition{}, err
	}
	digest := digests[d.Path]
	if d.Path != "" && digest == "" {
		return store.Definition{}, fmt.Errorf("%s isn't among the snapshot's files", d.Path)
	}
	manufacturer, _ := manufacturerName(d)
	return store.Definition{
		DefinitionKey: key,
		Manufacturer:  manufacturer,
		Model:         stringField(d.Identity["model"]),
		Slug:          stringField(d.Object.Fields["slug"]),
		PartNumber:    stringField(d.Object.Fields["part_number"]),
		Digest:        digest,
		JSON:          data,
	}, nil
}

// definitionKey returns the key the database keeps a definition by.
func definitionKey(d format.Definition) (store.DefinitionKey, error) {
	identity, err := json.Marshal(d.Identity)
	if err != nil {
		return store.DefinitionKey{}, fmt.Errorf("write the identity of a %s: %w", d.Object.Type, err)
	}
	return store.DefinitionKey{ObjectType: d.Object.Type, Path: d.Path, Identity: string(identity)}, nil
}

// stringField returns a string field's value, or "" for any other.
func stringField(v model.Value) string {
	s, _ := v.(model.String)
	return string(s)
}
