package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model/dcim"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/netbox"
)

func newNetBoxCommand(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "netbox",
		Short: "Work with the NetBox that TrueUp keeps true",
	}
	cmd.AddCommand(newNetBoxCheckCommand(a), newNetBoxBranchesCommand(a))
	return cmd
}

func newNetBoxCheckCommand(a *app) *cobra.Command {
	var (
		out    output
		branch string
	)
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Check that TrueUp can work with NetBox, and read all it needs",
		Long: `Connect to NetBox and check that it runs a release TrueUp supports
(NetBox ` + netbox.SupportedVersion + `) and accepts the token. Then check that the token can read
every object type TrueUp reads: manufacturers, device types and their
component templates, and with netbox.branching on, NetBox's branches. It
reads at most one object of each type, and writes nothing.

With netbox.branching on, NetBox must have netbox-branching ` + netbox.SupportedBranching + `.x. With it off,
NetBox having netbox-branching is a warning: TrueUp reads and writes main
only. With --branch, every object type is read through that branch, which
must be ready.

Exit codes:
  0  NetBox is supported, and the token can read every object type.
  1  The check couldn't be made: NetBox can't be reached, runs a release
     TrueUp doesn't support, or refuses the token; netbox.branching is on
     and NetBox hasn't netbox-branching ` + netbox.SupportedBranching + `.x; or the branch --branch
     names doesn't exist or isn't ready, or can't be found, since the
     token can't view NetBox's branches.
  2  --branch is given with netbox.branching off, or white space alone.
  3  The configuration is invalid, or netbox.url or netbox.token isn't set.
  4  The token can't read some object types. Grant its user the view
     permission on them. The result is printed as usual.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.loadConfig(cmd)
			if err != nil {
				return err
			}
			if err := requireNetBox(cfg.Settings.NetBox); err != nil {
				return err
			}
			name, err := branchName(cmd, branch, cfg.Settings.NetBox)
			if err != nil {
				return err
			}
			result, err := checkNetBox(cmd.Context(), cfg, a.logger, name)
			if err != nil {
				return withBranchHint(err)
			}
			if out == outputJSON {
				err = writeJSON(cmd.OutOrStdout(), result)
			} else {
				err = writeCheckText(cmd.OutOrStdout(), result)
			}
			if err != nil {
				return err
			}
			if denied := result.denied(); len(denied) > 0 {
				return outcomeError{code: exitCannotRead, err: fmt.Errorf(
					"the token can't read %d of the %d object types TrueUp reads: grant its user the view permission on %s",
					len(denied), len(result.ObjectTypes), strings.Join(denied, ", "))}
			}
			return nil
		},
	}
	addOutputFlag(cmd, &out)
	addBranchFlag(cmd, &branch)
	return cmd
}

// requireNetBox reports the NetBox settings a command needs that aren't set.
func requireNetBox(nb config.NetBox) error {
	var missing []error
	if nb.URL == "" {
		missing = append(missing, errors.New("netbox.url isn't set: set --netbox-url, TRUEUP_NETBOX_URL or netbox.url in the config file"))
	}
	if !nb.Token.IsSet() {
		missing = append(missing, errors.New("netbox.token isn't set: set --netbox-token-file, TRUEUP_NETBOX_TOKEN_FILE, or netbox.token_file in the config file"))
	}
	if len(missing) > 0 {
		return configError{errors.Join(missing...)}
	}
	return nil
}

// checkJSON is the result of `netbox check`, printed as text or JSON. Its
// JSON fields are part of the CLI's public interface (ADR 0018).
type checkJSON struct {
	URL           string `json:"url"`
	NetBoxVersion string `json:"netbox_version"`
	APIVersion    string `json:"api_version"`
	User          string `json:"user"`
	TokenVersion  int    `json:"token_version"`
	// Branching is netbox.branching, and BranchingVersion the
	// netbox-branching NetBox has, or null.
	Branching        bool    `json:"branching"`
	BranchingVersion *string `json:"branching_version"`
	// Branch is the branch read through, or null for main.
	Branch      *branchRefJSON   `json:"branch"`
	OK          bool             `json:"ok"` // every object type is readable
	ObjectTypes []objectTypeJSON `json:"object_types"`
	Warnings    []warningJSON    `json:"warnings"`
}

type objectTypeJSON struct {
	ObjectType string `json:"object_type"`
	Endpoint   string `json:"endpoint"`
	Readable   bool   `json:"readable"`
}

// denied returns the object types the token can't read.
func (r checkJSON) denied() []string {
	var out []string
	for _, t := range r.ObjectTypes {
		if !t.Readable {
			out = append(out, t.ObjectType)
		}
	}
	return out
}

// checkNetBox connects to NetBox and checks what the token can read, through
// the branch named branch, or in main if it is empty. Warnings about the
// NetBox settings, which loadConfig has logged, are part of the result too.
func checkNetBox(ctx context.Context, cfg *config.Config, logger *slog.Logger, branch string) (checkJSON, error) {
	c, err := connectNetBox(ctx, cfg, logger)
	if err != nil {
		return checkJSON{}, err
	}
	if branch != "" {
		if c, err = inBranch(ctx, c, branch); err != nil {
			return checkJSON{}, err
		}
	}
	checks, err := c.CheckRead(ctx, readTypes(cfg.Settings.NetBox.Branching))
	if err != nil {
		return checkJSON{}, fmt.Errorf("check what the token can read: %w", err)
	}

	info := c.Info()
	result := checkJSON{
		URL:              info.URL,
		NetBoxVersion:    info.Version,
		APIVersion:       info.APIVersion,
		User:             info.User,
		TokenVersion:     info.TokenVersion,
		Branching:        info.Branching,
		BranchingVersion: optional(info.BranchingVersion),
		Branch:           branchRef(info),
		OK:               true,
		ObjectTypes:      []objectTypeJSON{},
		Warnings:         netboxWarnings(cfg, info),
	}
	for _, check := range checks {
		result.ObjectTypes = append(result.ObjectTypes, objectTypeJSON{
			ObjectType: check.Type.Name, Endpoint: check.Type.Endpoint, Readable: check.Readable,
		})
		result.OK = result.OK && check.Readable
	}
	return result, nil
}

// branchType is netbox-branching's branch, which TrueUp reads with
// netbox.branching on, to find the branch --branch names, and to list them.
var branchType = netbox.ObjectType{Name: "netbox_branching.branch", Endpoint: "/api/plugins/branching/branches/"}

// readTypes are the object types compare reads, as the specs say, in the
// order a sync would read them: every type with an endpoint. Port mappings
// need no permission of their own, since a front port template carries
// them, as rear_ports (FrontPortTemplateSerializer in
// netbox/dcim/api/serializers_/devicetype_components.py). With branching,
// NetBox's branches come last.
func readTypes(branching bool) []netbox.ObjectType {
	var types []netbox.ObjectType
	for _, s := range dcim.Specs() {
		if s.Endpoint != "" {
			types = append(types, netbox.ObjectType{Name: s.Type, Endpoint: s.Endpoint})
		}
	}
	if branching {
		types = append(types, branchType)
	}
	return types
}

// connectNetBox connects to the NetBox the settings name, which checks its
// release and the token.
func connectNetBox(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*netbox.Client, error) {
	nb := cfg.Settings.NetBox
	c, err := netbox.Connect(ctx, netbox.Options{
		URL:             nb.URL,
		Token:           nb.Token,
		CACerts:         nb.CACerts,
		Timeout:         nb.Timeout,
		MaxRequests:     nb.MaxRequests,
		Retries:         nb.Retries,
		MaxResponseSize: nb.MaxResponseSize,
		Branching:       nb.Branching,
		Logger:          logger,
	})
	if err != nil {
		return nil, fmt.Errorf("connect to NetBox at %s: %w", nb.URL, err)
	}
	return c, nil
}

func writeCheckText(w io.Writer, r checkJSON) error {
	var b strings.Builder
	fmt.Fprintf(&b, "NetBox:     %s, release %s (API %s)\n", r.URL, r.NetBoxVersion, r.APIVersion)
	fmt.Fprintf(&b, "Token:      v%d, for user %s\n", r.TokenVersion, cell(r.User))
	if r.Branching {
		fmt.Fprintf(&b, "Branching:  %s\n", branchingOn(r.BranchingVersion))
	}
	if r.Branch != nil {
		fmt.Fprintf(&b, "Branch:     %s (%s), ready\n", cell(r.Branch.Name), r.Branch.SchemaID)
	}
	b.WriteString("\n")
	// tabwriter keeps rows until Flush, which reports any error.
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "OBJECT TYPE\tENDPOINT\tREADABLE")
	for _, t := range r.ObjectTypes {
		readable := "no"
		if t.Readable {
			readable = "yes"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", t.ObjectType, t.Endpoint, readable)
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	if r.OK {
		b.WriteString("\nThe token can read every object type TrueUp reads. Constraints on its permissions\n" +
			"aren't checked: where they apply, TrueUp sees only the objects they allow.\n")
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
