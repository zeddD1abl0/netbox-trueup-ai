package main

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
)

// Exit codes, as ADR 0018 defines them.
const (
	exitOK      = 0
	exitFailure = 1 // the command failed
	exitUsage   = 2 // unknown command or flag, or bad arguments
	exitConfig  = 3 // the configuration is invalid or can't be read
)

// usageError is a mistake on the command line.
type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }
func (e usageError) Unwrap() error { return e.err }

// configError means the settings couldn't be loaded.
type configError struct{ err error }

func (e configError) Error() string { return e.err.Error() }
func (e configError) Unwrap() error { return e.err }

func exitCode(err error) int {
	if _, ok := errors.AsType[usageError](err); ok {
		return exitUsage
	}
	if _, ok := errors.AsType[configError](err); ok {
		return exitConfig
	}
	return exitFailure
}

// reportError writes a failed command's error to stderr: through the logger
// as a JSON record when JSON logging is configured, otherwise as text. Until
// the configuration has loaded, there is no logger and the error is text.
func (a *app) reportError(cmd *cobra.Command, err error, code int) {
	if a.logger != nil && a.logFormat == config.LogJSON {
		a.logger.Error("command failed", "error", err.Error(), "exit_code", code)
		return
	}
	_, _ = fmt.Fprintln(a.stderr, "Error:", err)
	if code == exitUsage && cmd != nil {
		_, _ = fmt.Fprintf(a.stderr, "Run '%s --help' for usage.\n", cmd.CommandPath())
	}
}
