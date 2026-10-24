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
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/netbox"
)

func newNetBoxCommand(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "netbox",
		Short: "Work with the NetBox that TrueUp keeps true",
	}
	cmd.AddCommand(newNetBoxCheckCommand(a))
	return cmd
}

func newNetBoxCheckCommand(a *app) *cobra.Command {
	var out output
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Check that TrueUp can work with NetBox, and read all it needs",
		Long: `Connect to NetBox and check that it runs a release TrueUp supports
(NetBox ` + netbox.SupportedVersion + `) and accepts the token. Then check that the token can read
every object type TrueUp reads: manufacturers, device types and their
component templates. It reads at most one object of each type, and writes
nothing.

Exit codes:
  0  NetBox is supported, and the token can read every object type.
  1  The check couldn't be made: NetBox can't be reached, runs a release
     TrueUp doesn't support, or refuses the token.
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
			result, err := checkNetBox(cmd.Context(), cfg, a.logger)
			if err != nil {
				return err
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
	URL           string           `json:"url"`
	NetBoxVersion string           `json:"netbox_version"`
	APIVersion    string           `json:"api_version"`
	User          string           `json:"user"`
	TokenVersion  int              `json:"token_version"`
	OK            bool             `json:"ok"` // every object type is readable
	ObjectTypes   []objectTypeJSON `json:"object_types"`
	Warnings      []warningJSON    `json:"warnings"`
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

// checkNetBox connects to NetBox and checks what the token can read.
// Warnings about the NetBox settings, which loadConfig has logged, are part
// of the result too.
func checkNetBox(ctx context.Context, cfg *config.Config, logger *slog.Logger) (checkJSON, error) {
	nb := cfg.Settings.NetBox
	c, err := netbox.Connect(ctx, netbox.Options{
		URL:             nb.URL,
		Token:           nb.Token,
		CACerts:         nb.CACerts,
		Timeout:         nb.Timeout,
		MaxRequests:     nb.MaxRequests,
		Retries:         nb.Retries,
		MaxResponseSize: nb.MaxResponseSize,
		Logger:          logger,
	})
	if err != nil {
		return checkJSON{}, fmt.Errorf("connect to NetBox at %s: %w", nb.URL, err)
	}
	checks, err := c.CheckRead(ctx, netbox.ReadTypes())
	if err != nil {
		return checkJSON{}, fmt.Errorf("check what the token can read: %w", err)
	}

	info := c.Info()
	result := checkJSON{
		URL:           info.URL,
		NetBoxVersion: info.Version,
		APIVersion:    info.APIVersion,
		User:          info.User,
		TokenVersion:  info.TokenVersion,
		OK:            true,
		ObjectTypes:   []objectTypeJSON{},
		Warnings:      []warningJSON{},
	}
	for _, check := range checks {
		result.ObjectTypes = append(result.ObjectTypes, objectTypeJSON{
			ObjectType: check.Type.Name, Endpoint: check.Type.Endpoint, Readable: check.Readable,
		})
		result.OK = result.OK && check.Readable
	}
	for _, w := range cfg.Warnings {
		if strings.HasPrefix(w.Key, "netbox.") {
			result.Warnings = append(result.Warnings, warningJSON{Key: w.Key, Message: w.Message})
		}
	}
	for _, w := range info.Warnings {
		result.Warnings = append(result.Warnings, warningJSON{Message: w})
	}
	return result, nil
}

func writeCheckText(w io.Writer, r checkJSON) error {
	var b strings.Builder
	fmt.Fprintf(&b, "NetBox:  %s, release %s (API %s)\n", r.URL, r.NetBoxVersion, r.APIVersion)
	fmt.Fprintf(&b, "Token:   v%d, for user %s\n\n", r.TokenVersion, cell(r.User))
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
