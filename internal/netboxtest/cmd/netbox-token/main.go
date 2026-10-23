// Command netbox-token provisions a v2 API token on the NetBox that
// `make netbox-up` starts, writes it to a file, and says how to point TrueUp
// at that NetBox. `make netbox-up` runs it; see internal/netboxtest/README.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/netboxtest"
)

func main() {
	url := flag.String("url", "", "the local NetBox's URL, such as http://127.0.0.1:8000")
	out := flag.String("out", "", "the file to write the token to")
	flag.Parse()
	if *url == "" || *out == "" || flag.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "usage: netbox-token -url http://127.0.0.1:PORT -out FILE")
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, *url, *out)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, url, out string) error {
	username, password, err := netboxtest.Superuser()
	if err != nil {
		return err
	}
	token, err := netboxtest.ProvisionToken(ctx, url, "make netbox-up")
	if err != nil {
		return err
	}
	path, err := filepath.Abs(out)
	if err != nil {
		return fmt.Errorf("token file: %w", err)
	}
	if err := writeFile(path, []byte(token.Reveal()+"\n")); err != nil {
		return err
	}

	fmt.Printf(`NetBox is running at %s.
Sign in as %s with the password %s.

A new v2 API token is in %s. To point TrueUp at this NetBox:

  export TRUEUP_NETBOX_URL=%s
  export TRUEUP_NETBOX_TOKEN_FILE=%s

Stop it and delete its data with: make netbox-down
`, url, username, password, path, url, path)
	return nil
}

// writeFile replaces path with data, readable only by its owner, so that
// TrueUp doesn't warn about it and a half-written token is never read.
func writeFile(path string, data []byte) (err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("token file: %w", err)
	}
	f, err := os.CreateTemp(dir, ".token-*")
	if err != nil {
		return fmt.Errorf("token file: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.Remove(f.Name()))
		}
	}()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("write the token file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write the token file: %w", err)
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("write the token file: %w", err)
	}
	return nil
}
