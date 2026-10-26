package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/telemetry"
)

// app is what commands need from the process. Tests supply their own.
type app struct {
	stdout, stderr    io.Writer
	environ           []string
	defaultConfigFile string
	// sourceRootCAs, if set, are trusted for Git servers' certificates
	// instead of the system's. Only tests set it.
	sourceRootCAs *x509.CertPool

	// Set by loadConfig.
	logger    *slog.Logger
	logFormat config.LogFormat
}

func newRootCommand(a *app) *cobra.Command {
	root := &cobra.Command{
		Use:   "netbox-trueup-ai",
		Short: "Keep NetBox true to the YAML you keep in Git",
		Long: `NetBox TrueUp AI keeps NetBox in step with YAML definitions held in Git
repositories. It is in early development.

Settings come from flags, then TRUEUP_ environment variables, then a YAML
config file, then defaults. Run "netbox-trueup-ai config show" to see the
effective settings and where each came from.`,
		SilenceErrors: true, // run reports errors, so that it can choose the exit code
		SilenceUsage:  true,
	}
	root.SetOut(a.stdout)
	root.SetErr(a.stderr)
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageError{err} })
	config.RegisterFlags(root.PersistentFlags())
	root.AddCommand(newVersionCommand(), newConfigCommand(a), newNetBoxCommand(a), newSourceCommand(a), newMigrateCommand(a))
	root.SetHelpCommand(newHelpCommand())
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	usageErrors(root)
	return root
}

// usageErrors makes every argument error in the tree a usage error,
// including in the completion commands that cobra adds, so that they exit
// with 2 (ADR 0018). Commands that only group others reject arguments as
// unknown commands, and print their help when run on their own.
func usageErrors(cmd *cobra.Command) {
	switch {
	case cmd.HasSubCommands():
		cmd.Args = commandArgs
		if !cmd.Runnable() {
			cmd.RunE = showHelp
		}
	case cmd.Args != nil:
		check := cmd.Args
		cmd.Args = func(c *cobra.Command, args []string) error {
			if err := check(c, args); err != nil {
				if _, ok := errors.AsType[usageError](err); ok {
					return err
				}
				return usageError{err}
			}
			return nil
		}
	}
	for _, sub := range cmd.Commands() {
		usageErrors(sub)
	}
}

// newHelpCommand replaces cobra's help command, which reports an unknown
// topic on stdout and exits 0.
func newHelpCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "help [command]",
		Short: "Help about any command",
		RunE: func(c *cobra.Command, args []string) error {
			cmd, rest, err := c.Root().Find(args)
			if err != nil {
				return usageError{err}
			}
			if len(rest) > 0 {
				return usageError{fmt.Errorf("unknown help topic %q", strings.Join(args, " "))}
			}
			return cmd.Help()
		},
	}
}

// loadConfig loads the settings and sets up logging from them. Commands that
// need settings call it first.
func (a *app) loadConfig(cmd *cobra.Command) (*config.Config, error) {
	cfg, err := config.Load(cmd.Context(), config.Input{
		Flags:             cmd.Flags(),
		Environ:           a.environ,
		DefaultConfigFile: a.defaultConfigFile,
	})
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		// A failure, not a configuration error.
		return nil, fmt.Errorf("interrupted while loading the configuration: %w", err)
	}
	if err != nil {
		return nil, configError{err}
	}
	logger, err := telemetry.NewLogger(a.stderr, cfg.Settings.Log)
	if err != nil {
		return nil, configError{err}
	}
	a.logger, a.logFormat = logger, cfg.Settings.Log.Format
	for _, w := range cfg.Warnings {
		if w.Key != "" {
			logger.Warn(w.Message, "setting", w.Key)
		} else {
			logger.Warn(w.Message)
		}
	}
	logger.Debug("configuration loaded", "file", cfg.File.Path)
	return cfg, nil
}

// commandArgs is the Args check for commands that only group other
// commands: any argument is an unknown command.
func commandArgs(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	msg := fmt.Sprintf("unknown command %q for %q", args[0], cmd.CommandPath())
	if cmd.SuggestionsMinimumDistance <= 0 {
		cmd.SuggestionsMinimumDistance = 2 // cobra's own default, which SuggestionsFor doesn't apply
	}
	if suggestions := cmd.SuggestionsFor(args[0]); len(suggestions) > 0 {
		msg += fmt.Sprintf("; did you mean %q?", suggestions[0])
	}
	return usageError{errors.New(msg)}
}

// noArgs is the Args check for commands that take no arguments.
func noArgs(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	return usageError{fmt.Errorf("%q takes no arguments, but got %q", cmd.CommandPath(), args[0])}
}

// showHelp is the RunE of commands that only group other commands.
func showHelp(cmd *cobra.Command, _ []string) error {
	return cmd.Help()
}

// output is the value of a command's --output flag.
type output string

const (
	outputText output = "text"
	outputJSON output = "json"
)

// addOutputFlag adds --output to a command that prints a result.
func addOutputFlag(cmd *cobra.Command, o *output) {
	*o = outputText
	cmd.Flags().VarP(o, "output", "o", "output format: text or json")
}

func (o *output) String() string { return string(*o) }

func (o *output) Set(s string) error {
	switch v := output(s); v {
	case outputText, outputJSON:
		*o = v
		return nil
	default:
		return errors.New("must be text or json")
	}
}

func (o *output) Type() string { return "format" }

// writeJSON writes v as one indented JSON document.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}
