package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/source"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/source/git"
)

func newSourceCommand(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "source",
		Short: "Work with the sources that definitions come from",
	}
	cmd.AddCommand(newSourceFetchCommand(a))
	return cmd
}

func newSourceFetchCommand(a *app) *cobra.Command {
	var out output
	cmd := &cobra.Command{
		Use:   "fetch [source...]",
		Short: "Fetch the latest version of the configured sources",
		Long: `Fetch the latest version of every configured source, or of those named, into
the data directory's cache, and report each one's version and the files it
holds once its include and exclude patterns apply. A Git source's version is
the latest commit of its branch or tag. A source that hasn't changed since it
was last fetched isn't downloaded again.

Files larger than a source's max_file_size, symbolic links, submodules and
entries with unsafe names are skipped and reported. Caches of sources that
are no longer configured are removed.

Exit codes:
  0  Every source was fetched.
  1  The command couldn't run, such as when the data directory can't be
     written to.
  2  A name given isn't a configured source.
  3  The configuration is invalid, or no sources are configured.
  4  Some sources couldn't be fetched. The result is printed as usual, with
     each one's error, and the cache keeps their previous versions.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.loadConfig(cmd)
			if err != nil {
				return err
			}
			sources, err := chooseSources(cfg.Settings.Sources, args)
			if err != nil {
				return err
			}
			result, err := a.fetchSources(cmd.Context(), cfg, sources)
			if err != nil {
				return err
			}
			if out == outputJSON {
				err = writeJSON(cmd.OutOrStdout(), result)
			} else {
				err = writeFetchText(cmd.OutOrStdout(), result)
			}
			if err != nil {
				return err
			}
			if failed := result.failed(); len(failed) > 0 {
				return outcomeError{code: exitFetchFailed, err: fmt.Errorf(
					"%d of the %d sources couldn't be fetched: %s", len(failed), len(result.Sources), strings.Join(failed, ", "))}
			}
			return nil
		},
	}
	addOutputFlag(cmd, &out)
	return cmd
}

// chooseSources returns the configured sources that names name, in the
// order configured, or all of them if names is empty.
func chooseSources(configured []config.Source, names []string) ([]config.Source, error) {
	if len(configured) == 0 {
		return nil, configError{errors.New("no sources are configured: add them to the sources list in the config file, or set TRUEUP_SOURCES")}
	}
	if len(names) == 0 {
		return configured, nil
	}
	var known []string
	for _, s := range configured {
		known = append(known, s.Name)
	}
	for _, name := range names {
		if !slices.Contains(known, name) {
			return nil, usageError{fmt.Errorf("%q isn't a configured source: the sources are %s", name, strings.Join(known, ", "))}
		}
	}
	var out []config.Source
	for _, s := range configured {
		if slices.Contains(names, s.Name) {
			out = append(out, s)
		}
	}
	return out, nil
}

// fetchJSON is the result of `source fetch`, printed as text or JSON. Its
// JSON fields are part of the CLI's public interface (ADR 0018).
type fetchJSON struct {
	OK      bool              `json:"ok"` // every source was fetched
	Sources []fetchSourceJSON `json:"sources"`
}

type fetchSourceJSON struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	URL  string `json:"url"`
	// Ref is the branch or tag followed, the default branch's name when the
	// source sets none, or null when the fetch failed.
	Ref     *string       `json:"ref"`
	OK      bool          `json:"ok"`
	Version *string       `json:"version"`
	Files   *int          `json:"files"`
	Skipped []skippedJSON `json:"skipped"`
	Error   *string       `json:"error"`
}

type skippedJSON struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// failed returns the names of the sources that couldn't be fetched.
func (r fetchJSON) failed() []string {
	var out []string
	for _, s := range r.Sources {
		if !s.OK {
			out = append(out, s.Name)
		}
	}
	return out
}

// fetchSources fetches each source in turn. A source that fails is reported
// in the result; only a problem with the cache as a whole, or an interrupt,
// is an error.
func (a *app) fetchSources(ctx context.Context, cfg *config.Config, sources []config.Source) (fetchJSON, error) {
	root := filepath.Join(cfg.Settings.DataDir, "cache", "sources")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fetchJSON{}, fmt.Errorf("create the source cache in the data directory: %w", err)
	}
	kinds := map[string]source.Kind{
		config.SourceKindGit: git.New(git.Options{CacheDir: root, RootCAs: a.sourceRootCAs, Logger: a.logger}),
	}
	result := fetchJSON{OK: true, Sources: []fetchSourceJSON{}}
	for _, s := range sources {
		row := fetchSourceJSON{Name: s.Name, Kind: s.Kind, URL: s.URL, Skipped: []skippedJSON{}}
		if err := fetchSource(ctx, kinds, s, &row); err != nil {
			if ctx.Err() != nil {
				return fetchJSON{}, fmt.Errorf("interrupted while fetching source %s: %w", s.Name, ctx.Err())
			}
			msg := err.Error()
			row.Error = &msg
			result.OK = false
			a.logger.Warn("can't fetch source", "source", s.Name, "error", msg)
		}
		result.Sources = append(result.Sources, row)
	}

	var keep []string
	for _, s := range cfg.Settings.Sources {
		keep = append(keep, s.Name)
	}
	removed, err := source.PruneCache(ctx, root, keep)
	for _, name := range removed {
		a.logger.Info("removed the cache of a source no longer configured", "source", name)
	}
	if err != nil {
		a.logger.Warn("can't remove the caches of sources no longer configured", "error", err)
	}
	return result, nil
}

// fetchSource fetches one source and fills in its row.
func fetchSource(ctx context.Context, kinds map[string]source.Kind, s config.Source, row *fetchSourceJSON) error {
	kind, ok := kinds[s.Kind]
	if !ok {
		return fmt.Errorf("TrueUp can't fetch sources of kind %q", s.Kind)
	}
	spec, err := source.NewSpec(s)
	if err != nil {
		return err
	}
	snap, err := source.Fetch(ctx, kind, spec)
	if err != nil {
		return err
	}
	defer func() { _ = snap.Close() }()
	files := len(snap.Files)
	row.OK = true
	row.Ref, row.Version, row.Files = &snap.Ref, &snap.Version, &files
	for _, sk := range snap.Skipped {
		row.Skipped = append(row.Skipped, skippedJSON{Path: sk.Path, Reason: string(sk.Reason)})
	}
	return nil
}

// skipDescriptions say what was skipped for each reason, for text output.
var skipDescriptions = map[source.Reason]string{
	source.TooLarge:   "larger than max_file_size",
	source.Symlink:    "symbolic links",
	source.Submodule:  "submodules",
	source.UnsafePath: "with unsafe names",
	source.NotAFile:   "not files",
}

func writeFetchText(w io.Writer, r fetchJSON) error {
	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "SOURCE\tREF\tVERSION\tFILES\tSKIPPED")
	for _, s := range r.Sources {
		if !s.OK {
			_, _ = fmt.Fprintf(tw, "%s\t-\t-\t-\t-\n", s.Name)
			continue
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\n", s.Name, cell(*s.Ref), *s.Version, *s.Files, len(s.Skipped))
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("write output: %w", err)
	}

	var skips []string
	for _, s := range r.Sources {
		var reasons []source.Reason
		count := map[source.Reason]int{}
		example := map[source.Reason]string{}
		for _, sk := range s.Skipped {
			reason := source.Reason(sk.Reason)
			if count[reason] == 0 {
				reasons = append(reasons, reason)
				example[reason] = sk.Path
			}
			count[reason]++
		}
		slices.Sort(reasons)
		for _, reason := range reasons {
			skips = append(skips, fmt.Sprintf("  %s: %d %s, such as %s", s.Name, count[reason], skipDescriptions[reason], cell(example[reason])))
		}
	}
	if len(skips) > 0 {
		b.WriteString("\nSkipped files (--output json lists them all):\n")
		b.WriteString(strings.Join(skips, "\n") + "\n")
	}
	if failed := r.failed(); len(failed) > 0 {
		b.WriteString("\nErrors:\n")
		for _, s := range r.Sources {
			if s.Error != nil {
				fmt.Fprintf(&b, "  %s: %s\n", s.Name, cell(*s.Error))
			}
		}
	}
	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}
