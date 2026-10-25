package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
)

func newConfigCommand(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect the configuration",
	}
	cmd.AddCommand(newConfigShowCommand(a))
	return cmd
}

func newConfigShowCommand(a *app) *cobra.Command {
	var out output
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Print the effective settings and where each came from",
		Long: `Print every setting's effective value and the layer it came from: a flag,
an environment variable, the config file or the default. Secrets are shown
as [redacted], with the file they were read from if they came from one.

The configuration is validated first, and every problem is reported.
Warnings, such as a secret file every user can read, are printed after the
settings whatever the log level.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.loadConfig(cmd)
			if err != nil {
				return err
			}
			if out == outputJSON {
				return writeJSON(cmd.OutOrStdout(), newShowJSON(cfg))
			}
			return writeShowText(cmd.OutOrStdout(), cfg)
		},
	}
	addOutputFlag(cmd, &out)
	return cmd
}

// showJSON is the JSON form of `config show`. Its fields are part of the
// CLI's public interface (ADR 0018).
type showJSON struct {
	ConfigFile *fileJSON     `json:"config_file"` // null if no file was read
	Settings   []settingJSON `json:"settings"`
	Warnings   []warningJSON `json:"warnings"`
}

type warningJSON struct {
	Key     string `json:"key,omitempty"`
	Message string `json:"message"`
}

type fileJSON struct {
	Path   string `json:"path"`
	Origin string `json:"origin"`
}

type settingJSON struct {
	Key    string `json:"key"`
	Value  any    `json:"value"`
	Secret bool   `json:"secret,omitempty"`
	Origin string `json:"origin"`
	Name   string `json:"name,omitempty"`
	File   string `json:"file,omitempty"`
}

func newShowJSON(cfg *config.Config) showJSON {
	out := showJSON{Settings: []settingJSON{}, Warnings: []warningJSON{}}
	if cfg.File.Path != "" {
		out.ConfigFile = &fileJSON{Path: cfg.File.Path, Origin: cfg.File.Origin.String()}
	}
	for _, w := range cfg.Warnings {
		out.Warnings = append(out.Warnings, warningJSON{Key: w.Key, Message: w.Message})
	}
	for _, e := range cfg.Entries() {
		out.Settings = append(out.Settings, settingJSON{
			Key:    e.Key,
			Value:  e.Value,
			Secret: e.Secret,
			Origin: e.Origin.String(),
			Name:   e.Name,
			File:   e.File,
		})
	}
	return out
}

func writeShowText(w io.Writer, cfg *config.Config) error {
	rows := [][3]string{{"SETTING", "FROM", "VALUE"}}
	for _, e := range cfg.Entries() {
		// Values vary most in width, so they go last.
		add := func(key, value string) {
			if value == "" {
				value = "(not set)"
			}
			rows = append(rows, [3]string{key, describeOrigin(e), value})
		}
		switch v := e.Value.(type) {
		case []config.Source:
			if len(v) == 0 {
				add(e.Key, "[]")
			}
			for i, s := range v {
				for _, f := range [][2]string{
					{"name", s.Name},
					{"kind", s.Kind},
					{"url", s.URL},
					{"ref", s.Ref},
					{"format", s.Format},
					{"include", list(s.Include)},
					{"exclude", list(s.Exclude)},
					{"max_file_size", s.MaxFileSize},
					{"max_files", s.MaxFiles},
					{"fetch_timeout", s.FetchTimeout},
					{"max_fetch_size", s.MaxFetchSize},
				} {
					add(fmt.Sprintf("%s[%d].%s", e.Key, i, f[0]), f[1])
				}
			}
		case string:
			if e.File != "" {
				v += " (read from " + e.File + ")"
			}
			add(e.Key, v)
		default:
			add(e.Key, fmt.Sprint(v))
		}
	}

	if _, err := fmt.Fprintf(w, "Config file: %s\n\n", describeFile(cfg.File)); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, r := range rows {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\n", cell(r[0]), cell(r[1]), cell(r[2])); err != nil {
			return fmt.Errorf("write output: %w", err)
		}
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	if len(cfg.Warnings) == 0 {
		return nil
	}
	if _, err := fmt.Fprintln(w, "\nWarnings:"); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	for _, warning := range cfg.Warnings {
		if _, err := fmt.Fprintln(w, "  "+cell(warning.Message)); err != nil {
			return fmt.Errorf("write output: %w", err)
		}
	}
	return nil
}

func describeFile(f config.File) string {
	if f.Path == "" {
		return "none"
	}
	switch f.Origin {
	case config.OriginFlag:
		return f.Path + " (from --" + config.FlagConfig + ")"
	case config.OriginEnv:
		return f.Path + " (from " + config.EnvConfigFile + ")"
	case config.OriginDefault, config.OriginFile:
		return f.Path + " (default location)"
	}
	return f.Path
}

func describeOrigin(e config.Entry) string {
	switch e.Origin {
	case config.OriginFlag:
		return "flag " + e.Name
	case config.OriginEnv:
		return "env " + e.Name
	case config.OriginFile:
		if e.Name != e.Key {
			return "file, as " + e.Name
		}
		return "file"
	case config.OriginDefault:
		return "default"
	}
	return e.Origin.String()
}

// list formats a list of strings as JSON, which keeps empty lists and
// patterns containing commas unambiguous.
func list(items []string) string {
	b, err := json.Marshal(items)
	if err != nil {
		return fmt.Sprint(items)
	}
	return string(b)
}

// cell quotes a value holding characters that would break the table or
// mislead the terminal (see unsafeRune). strconv.Quote escapes them all.
func cell(s string) string {
	if strings.ContainsFunc(s, unsafeRune) {
		return strconv.Quote(s)
	}
	return s
}
