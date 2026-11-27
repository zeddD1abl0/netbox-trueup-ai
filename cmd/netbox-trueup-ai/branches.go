package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/netbox"
)

// branchUsage is the help of --branch, on each command that reads NetBox.
const branchUsage = "read NetBox through the branch with this `name`, exactly, case included, which must be ready; needs netbox.branching on"

// addBranchFlag adds --branch to a command that reads NetBox.
func addBranchFlag(cmd *cobra.Command, name *string) {
	cmd.Flags().StringVar(name, "branch", "", branchUsage)
}

// branchName returns the branch --branch names, as NetBox stores a name,
// without white space at either end, or "" for main when it isn't given. It
// is a usage error to give it white space alone, or with netbox.branching
// off.
func branchName(cmd *cobra.Command, value string, nb config.NetBox) (string, error) {
	if !cmd.Flags().Changed("branch") {
		return "", nil
	}
	name := model.StripSpace(value)
	switch {
	case name == "":
		return "", usageError{fmt.Errorf("--branch needs a branch's name, not %s", strconv.Quote(value))}
	case !nb.Branching:
		return "", usageError{errors.New("--branch reads through a NetBox branch, which needs netbox.branching on")}
	}
	return name, nil
}

// inBranch returns a client that reads through NetBox's branch named name,
// once NetBox has it and it is ready.
func inBranch(ctx context.Context, c *netbox.Client, name string) (*netbox.Client, error) {
	b, err := c.FindBranch(ctx, name)
	switch {
	case errors.Is(err, netbox.ErrNoBranch):
		return nil, fmt.Errorf("NetBox has no branch %s: 'netbox-trueup-ai netbox branches' lists those TrueUp can use", strconv.Quote(name))
	case err != nil:
		return nil, fmt.Errorf("find branch %s: %w", strconv.Quote(name), err)
	case !b.Ready():
		return nil, fmt.Errorf("branch %s is %s, not ready: TrueUp reads only branches that are ready; 'netbox-trueup-ai netbox branches' lists those it can use",
			strconv.Quote(name), cell(b.Status))
	}
	bc, err := c.InBranch(ctx, b)
	if err != nil {
		return nil, fmt.Errorf("read through branch %s: %w", strconv.Quote(name), err)
	}
	return bc, nil
}

// withBranchHint adds to an error that is NetBox refusing a branch, which
// was ready when the command chose it, where to see the branch's status.
func withBranchHint(err error) error {
	if errors.Is(err, netbox.ErrBranchRefused) {
		return fmt.Errorf("%w: 'netbox-trueup-ai netbox branches --all' shows its status", err)
	}
	return err
}

// branchRefJSON names the branch a command read through. Its JSON fields are
// part of the CLI's public interface (ADR 0018).
type branchRefJSON struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	SchemaID string `json:"schema_id"`
}

// branchRef returns the branch a client reads through, or nil for main.
func branchRef(info netbox.Info) *branchRefJSON {
	if info.Branch == nil {
		return nil
	}
	return &branchRefJSON{ID: info.Branch.ID, Name: info.Branch.Name, SchemaID: info.Branch.SchemaID}
}

// optional returns s, or nil if it is empty, for JSON's null.
func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func newNetBoxBranchesCommand(a *app) *cobra.Command {
	var (
		out output
		all bool
	)
	cmd := &cobra.Command{
		Use:   "branches",
		Short: "List the targets a run can have: main, or NetBox's branches",
		Long: `List what a run can target in NetBox: NetBox's branches that are ready, from
netbox-branching, and main where the settings allow it. With --all, list
every branch, whatever its status, and mark those that aren't targets.

With netbox.branching off, the default, every run reads and writes main, and
main is all it lists, without reading NetBox's branches. With it on, main is
listed only when netbox.protect_main is off. A branch that isn't ready, such
as one being synchronized or merged, can't be used until it is again.

Listing branches needs the view permission on netbox_branching.branch.

Exit codes:
  0  The targets are listed, none included.
  1  The command couldn't run: NetBox can't be reached, runs a release
     TrueUp doesn't support, refuses the token, or the token can't view
     its branches, or netbox.branching is on and NetBox hasn't
     netbox-branching ` + netbox.SupportedBranching + `.x.
  3  The configuration is invalid, or netbox.url or netbox.token isn't set.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.loadConfig(cmd)
			if err != nil {
				return err
			}
			if err := requireNetBox(cfg.Settings.NetBox); err != nil {
				return err
			}
			result, err := listBranches(cmd.Context(), cfg, a, all)
			if err != nil {
				return err
			}
			if out == outputJSON {
				return writeJSON(cmd.OutOrStdout(), result)
			}
			return writeBranchesText(cmd.OutOrStdout(), result, all)
		},
	}
	addOutputFlag(cmd, &out)
	cmd.Flags().BoolVar(&all, "all", false, "list every branch, whatever its status, not only those that are ready")
	return cmd
}

// branchesJSON is the result of `netbox branches`, printed as text or JSON.
// Its JSON fields are part of the CLI's public interface (ADR 0018).
type branchesJSON struct {
	URL           string `json:"url"`
	NetBoxVersion string `json:"netbox_version"`
	// Branching and ProtectMain are the settings, and BranchingVersion the
	// netbox-branching NetBox has, or null.
	Branching        bool    `json:"branching"`
	BranchingVersion *string `json:"branching_version"`
	ProtectMain      bool    `json:"protect_main"`
	// Main says main is a target.
	Main bool `json:"main"`
	// Branches are the branches that are targets, or with --all every
	// branch, in NetBox's ID order.
	Branches []branchJSON `json:"branches"`
	// NotReady counts the branches that aren't ready, by status.
	NotReady map[string]int `json:"not_ready"`
	Warnings []warningJSON  `json:"warnings"`
}

type branchJSON struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	SchemaID string `json:"schema_id"`
	Status   string `json:"status"`
	// Target says a run can target it: it is ready.
	Target      bool       `json:"target"`
	Owner       *string    `json:"owner"`
	Description string     `json:"description"`
	LastSync    *time.Time `json:"last_sync"`
}

// listBranches connects to NetBox and lists the targets a run can have.
func listBranches(ctx context.Context, cfg *config.Config, a *app, all bool) (branchesJSON, error) {
	nb := cfg.Settings.NetBox
	c, err := connectNetBox(ctx, cfg, a.logger)
	if err != nil {
		return branchesJSON{}, err
	}
	info := c.Info()
	result := branchesJSON{
		URL: info.URL, NetBoxVersion: info.Version,
		Branching: nb.Branching, BranchingVersion: optional(info.BranchingVersion), ProtectMain: nb.ProtectMain,
		Main:     !nb.Branching || !nb.ProtectMain,
		Branches: []branchJSON{}, NotReady: map[string]int{}, Warnings: netboxWarnings(cfg, info),
	}
	if !nb.Branching {
		return result, nil
	}
	branches, err := c.Branches(ctx)
	if err != nil {
		return branchesJSON{}, fmt.Errorf("list NetBox's branches: %w", err)
	}
	for _, b := range branches {
		if !b.Ready() {
			result.NotReady[b.Status]++
			if !all {
				continue
			}
		}
		row := branchJSON{
			ID: b.ID, Name: b.Name, SchemaID: b.SchemaID, Status: b.Status, Target: b.Ready(),
			Owner: optional(b.Owner), Description: b.Description,
		}
		if !b.LastSync.IsZero() {
			synced := b.LastSync
			row.LastSync = &synced
		}
		result.Branches = append(result.Branches, row)
	}
	return result, nil
}

// netboxWarnings are the warnings about the NetBox settings, which loadConfig
// has logged, and the client's, which Connect has logged.
func netboxWarnings(cfg *config.Config, info netbox.Info) []warningJSON {
	out := []warningJSON{}
	for _, w := range cfg.Warnings {
		if strings.HasPrefix(w.Key, "netbox.") {
			out = append(out, warningJSON{Key: w.Key, Message: w.Message})
		}
	}
	for _, w := range info.Warnings {
		out = append(out, warningJSON{Key: w.Key, Message: w.Message})
	}
	return out
}

// branchingOn says that branching is on, with the netbox-branching NetBox
// has, for the head of a command's text output.
func branchingOn(version *string) string {
	if version == nil {
		return "on"
	}
	return "on, netbox-branching " + cell(*version)
}

// branchingText says what the branching settings make of NetBox's targets,
// for the head of `netbox branches`.
func branchingText(branching bool, version *string, protectMain bool) string {
	if !branching {
		return "off (netbox.branching), so main is the only target"
	}
	text := branchingOn(version)
	if protectMain {
		return text + "; main is protected (netbox.protect_main), so runs use branches only"
	}
	return text + "; main isn't protected (netbox.protect_main), so runs may use main or a branch"
}

func writeBranchesText(w io.Writer, r branchesJSON, all bool) error {
	var b strings.Builder
	fmt.Fprintf(&b, "NetBox:     %s, release %s\n", cell(r.URL), cell(r.NetBoxVersion))
	fmt.Fprintf(&b, "Branching:  %s\n\n", branchingText(r.Branching, r.BranchingVersion, r.ProtectMain))
	if !r.Main && len(r.Branches) == 0 {
		b.WriteString("No branch is ready: create one in NetBox, or wait until one is.\n")
	} else {
		tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
		header := "TARGET\tNAME\tSCHEMA ID\tOWNER\tLAST SYNC\tDESCRIPTION"
		if all {
			header = "TARGET\tNAME\tSCHEMA ID\tSTATUS\tOWNER\tLAST SYNC\tDESCRIPTION"
		}
		_, _ = fmt.Fprintln(tw, header)
		if r.Main {
			if all {
				_, _ = fmt.Fprintln(tw, "main\t-\t-\t-\t-\t-\t-")
			} else {
				_, _ = fmt.Fprintln(tw, "main\t-\t-\t-\t-\t-")
			}
		}
		for _, br := range r.Branches {
			target, owner, synced, description := "-", "-", "-", "-"
			if br.Target {
				target = "branch"
			}
			if br.Owner != nil {
				owner = cell(*br.Owner)
			}
			if br.LastSync != nil {
				synced = br.LastSync.UTC().Format(time.RFC3339)
			}
			if br.Description != "" {
				description = cell(br.Description)
			}
			if all {
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", target, cell(br.Name), br.SchemaID, cell(br.Status), owner, synced, description)
			} else {
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", target, cell(br.Name), br.SchemaID, owner, synced, description)
			}
		}
		if err := tw.Flush(); err != nil {
			return fmt.Errorf("write output: %w", err)
		}
	}
	if n := countNotReady(r.NotReady); n > 0 && !all {
		var parts []string
		for _, status := range slices.Sorted(maps.Keys(r.NotReady)) {
			parts = append(parts, fmt.Sprintf("%d %s", r.NotReady[status], cell(status)))
		}
		other := "branches aren't"
		if n == 1 {
			other = "branch isn't"
		}
		fmt.Fprintf(&b, "\n%d other %s ready: %s. --all lists them.\n", n, other, strings.Join(parts, ", "))
	}
	if len(r.Warnings) > 0 {
		b.WriteString("\nWarnings:\n")
		for _, warning := range r.Warnings {
			fmt.Fprintf(&b, "  %s\n", cell(warning.Message))
		}
	}
	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

func countNotReady(byStatus map[string]int) int {
	n := 0
	for _, c := range byStatus {
		n += c
	}
	return n
}
