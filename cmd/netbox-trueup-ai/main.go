// Command netbox-trueup-ai keeps NetBox in step with YAML definitions held in
// Git repositories.
//
// The project is in early development: for now the command reports its
// version, shows its configuration, checks that it can read what it needs
// from NetBox, and fetches sources. Every command follows the conventions in
// docs/adr/0018-cli-conventions.md.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	// The first signal cancels ctx. Restoring the default handling then
	// lets a second one stop the process at once, even if it is stuck in a
	// read that ctx can't interrupt, such as a secret file on a hung mount.
	go func() {
		<-ctx.Done()
		stop()
	}()
	a := &app{
		stdout:            os.Stdout,
		stderr:            os.Stderr,
		environ:           os.Environ(),
		defaultConfigFile: config.DefaultConfigFile(),
	}
	code := run(ctx, a, os.Args[1:])
	stop()
	os.Exit(code)
}

// run executes the command line in args and returns the exit code.
func run(ctx context.Context, a *app, args []string) int {
	root := newRootCommand(a) //nolint:contextcheck // ExecuteContextC below gives ctx to every command, which reads it with cmd.Context()
	// A non-nil slice, so that cobra never falls back to os.Args.
	root.SetArgs(append([]string{}, args...))
	cmd, err := root.ExecuteContextC(ctx)
	if err == nil {
		return exitOK
	}
	code := exitCode(err)
	a.reportError(cmd, err, code)
	return code
}
