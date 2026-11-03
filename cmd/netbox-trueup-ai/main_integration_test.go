//go:build integration

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/netboxtest"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store/postgres/pgtest"
)

// pg and nb are the PostgreSQL and the NetBox that TestMain starts, for the
// tests to share.
var (
	pg *pgtest.Server
	nb *netboxtest.NetBox
)

func TestMain(m *testing.M) {
	os.Exit(runWithServices(m))
}

// runWithServices starts PostgreSQL and NetBox, at once, since NetBox takes
// minutes, runs the tests, and stops both.
func runWithServices(m *testing.M) int {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	var pgErr, nbErr error
	var wg sync.WaitGroup
	wg.Go(func() { pg, pgErr = pgtest.Start(ctx) })
	wg.Go(func() { nb, nbErr = netboxtest.Start(ctx) })
	wg.Wait()
	code := 1
	if err := errors.Join(pgErr, nbErr); err != nil {
		fmt.Fprintln(os.Stderr, "start the services the tests need:", err)
	} else {
		code = m.Run()
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer stopCancel()
	if pg != nil {
		if err := pg.Stop(stopCtx); err != nil {
			fmt.Fprintln(os.Stderr, "stop PostgreSQL:", err)
			code = 1
		}
	}
	if nb != nil {
		if err := nb.Stop(stopCtx); err != nil {
			fmt.Fprintln(os.Stderr, "stop NetBox:", err)
			code = 1
		}
	}
	return code
}
