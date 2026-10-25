package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
)

// Exit codes, as ADR 0018 defines them. Codes from 4 are particular to one
// command, and its help documents them.
const (
	exitOK      = 0
	exitFailure = 1 // the command failed
	exitUsage   = 2 // unknown command or flag, or bad arguments
	exitConfig  = 3 // the configuration is invalid or can't be read

	exitCannotRead  = 4 // netbox check: the token can't read every object type TrueUp reads
	exitFetchFailed = 4 // source fetch: some sources couldn't be fetched
)

// outcomeError is an outcome particular to one command, with an exit code of
// its own, from 4 (ADR 0018). The command has printed its result first.
type outcomeError struct {
	code int
	err  error
}

func (e outcomeError) Error() string { return e.err.Error() }
func (e outcomeError) Unwrap() error { return e.err }

// usageError is a mistake on the command line.
type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }
func (e usageError) Unwrap() error { return e.err }

// configError means the settings couldn't be loaded.
type configError struct{ err error }

func (e configError) Error() string { return e.err.Error() }
func (e configError) Unwrap() error { return e.err }

func exitCode(err error) int {
	if outcome, ok := errors.AsType[outcomeError](err); ok {
		return outcome.code
	}
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
	_, _ = fmt.Fprintln(a.stderr, "Error:", escapeControls(err.Error()))
	if code == exitUsage && cmd != nil {
		_, _ = fmt.Fprintf(a.stderr, "Run '%s --help' for usage.\n", cmd.CommandPath())
	}
}

// escapeControls escapes the characters in an error's message that could
// drive or mislead a terminal (see unsafeRune), and bytes that aren't UTF-8,
// since it can hold text from NetBox, a certificate or a Git repository. Line breaks stay, for errors that list several
// problems.
func escapeControls(msg string) string {
	unsafe := func(r rune) bool { return r != '\n' && unsafeRune(r) }
	if utf8.ValidString(msg) && !strings.ContainsFunc(msg, unsafe) {
		return msg
	}
	var b strings.Builder
	for len(msg) > 0 {
		r, size := utf8.DecodeRuneInString(msg)
		switch {
		case r == utf8.RuneError && size == 1:
			// A byte that isn't UTF-8, such as 0x9b, which some
			// terminals read as a control sequence.
			fmt.Fprintf(&b, `\x%02x`, msg[0])
		case unsafe(r):
			q := strconv.QuoteRune(r)
			b.WriteString(q[1 : len(q)-1])
		default:
			b.WriteString(msg[:size])
		}
		msg = msg[size:]
	}
	return b.String()
}

// unsafeRune reports whether r shouldn't reach a terminal as it is: a control
// character, or a format character, such as a bidirectional override or a
// zero-width space, which can make text read as something it isn't.
func unsafeRune(r rune) bool {
	return unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
}
