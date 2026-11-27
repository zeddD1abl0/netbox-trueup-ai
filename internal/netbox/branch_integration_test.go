//go:build integration

package netbox

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/netboxtest"
)

// needsBranching skips a test on the stock NetBox, which has no
// netbox-branching.
func needsBranching(t *testing.T) {
	t.Helper()
	if nb.Profile != netboxtest.Branching {
		t.Skip("needs netbox-branching: run with NETBOXTEST_PROFILE=branching")
	}
}

// Connect reads NetBox's plugins: with netbox.branching on, the stock NetBox
// is refused and the branching one taken; off, the branching one is a
// warning, and the stock one says nothing.
func TestBranchingInNetBox(t *testing.T) {
	t.Parallel()
	for _, branching := range []bool{false, true} {
		c, err := Connect(t.Context(), Options{URL: nb.URL, Token: nb.Token, Branching: branching})
		switch {
		case nb.Profile == netboxtest.Stock && branching:
			if be, ok := errors.AsType[*BranchingError](err); !ok || be.Installed || be.Unlisted {
				t.Errorf("Connect with branching on = %v, want a *BranchingError for a NetBox without netbox-branching", err)
			}
			continue
		case err != nil:
			t.Fatalf("Connect with branching %v: %v", branching, err)
		}
		info := c.Info()
		wantVersion, wantWarnings := "", 0
		if nb.Profile == netboxtest.Branching {
			wantVersion = netboxtest.BranchingVersion
			if !branching {
				wantWarnings = 1
			}
		}
		if info.BranchingVersion != wantVersion || len(info.Warnings) != wantWarnings || info.Branching != branching {
			t.Errorf("branching %v: Info = %+v, want netbox-branching %q and %d warnings", branching, info, wantVersion, wantWarnings)
		}
		if wantWarnings == 1 && info.Warnings[0].Key != "netbox.branching" {
			t.Errorf("warning %+v isn't about netbox.branching", info.Warnings[0])
		}
	}
}

// A client bound to a branch reads what the branch holds, and main's reads
// what main holds.
func TestReadThroughABranch(t *testing.T) {
	t.Parallel()
	needsBranching(t)
	ctx := t.Context()
	made, err := nb.CreateBranch(ctx, "client-lab", "TrueUp's client tests")
	if err != nil {
		t.Fatal(err)
	}
	var mfr struct{ ID int64 }
	if err := nb.InBranch(made).Post(ctx, "/api/dcim/manufacturers/", map[string]any{"name": "Client Branch Only", "slug": "client-branch-only"}, &mfr); err != nil {
		t.Fatal(err)
	}
	c, err := Connect(ctx, Options{URL: nb.URL, Token: nb.Token, Branching: true})
	if err != nil {
		t.Fatal(err)
	}

	b, err := c.FindBranch(ctx, "client-lab")
	if err != nil {
		t.Fatalf("FindBranch: %v", err)
	}
	if b.ID != int64(made.ID) || b.SchemaID != made.SchemaID || !b.Ready() || b.Owner == "" || b.Description != "TrueUp's client tests" {
		t.Errorf("FindBranch = %+v, want the ready branch %+v", b, made)
	}
	if _, err := c.FindBranch(ctx, "CLIENT-LAB"); !errors.Is(err, ErrNoBranch) {
		t.Errorf("FindBranch(CLIENT-LAB) error = %v, want ErrNoBranch: names are exact", err)
	}
	all, err := c.Branches(ctx)
	if err != nil || !slices.ContainsFunc(all, func(x Branch) bool { return x == b }) {
		t.Errorf("Branches = %+v, %v; want it to hold %+v", all, err, b)
	}

	bc, err := c.InBranch(t.Context(), b)
	if err != nil {
		t.Fatal(err)
	}
	has := func(client *Client) bool {
		found := false
		err := client.List(ctx, "/api/dcim/manufacturers/", nil, func(raw json.RawMessage) error {
			id, err := objectID(raw)
			found = found || id == mfr.ID
			return err
		})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		return found
	}
	if !has(bc) || has(c) {
		t.Errorf("the branch's manufacturer is in the branch %v, in main %v; want only the branch", has(bc), has(c))
	}
	if checks, err := bc.CheckRead(ctx, specTypes()); err != nil || len(checks) != len(specTypes()) {
		t.Errorf("CheckRead in the branch = %+v, %v", checks, err)
	}
}

// netbox-branching refuses a request that names a branch it hasn't got, with
// its plain-text 400, which the client reports as such.
func TestBranchRefusedByNetBox(t *testing.T) {
	t.Parallel()
	needsBranching(t)
	c, err := Connect(t.Context(), Options{URL: nb.URL, Token: nb.Token, Branching: true})
	if err != nil {
		t.Fatal(err)
	}
	gone, err := c.InBranch(t.Context(), Branch{ID: 999, Name: "gone", SchemaID: "zzzzzzzz", Status: BranchReady})
	if err != nil {
		t.Fatal(err)
	}
	err = gone.List(t.Context(), "/api/dcim/manufacturers/", nil, func(json.RawMessage) error { return nil })
	if re, ok := errors.AsType[*BranchRefusedError](err); !ok || re.Message != "Invalid branch identifier" {
		t.Errorf("List in a branch NetBox hasn't got: %v, want a *BranchRefusedError", err)
	}
	// The branches themselves are read from main, so they still list.
	if _, err := gone.Branches(t.Context()); err != nil {
		t.Errorf("Branches from that client: %v", err)
	}
}

// Listing branches needs the view permission on netbox_branching.branch.
func TestBranchesNeedTheViewPermission(t *testing.T) {
	t.Parallel()
	needsBranching(t)
	c, err := Connect(t.Context(), Options{URL: nb.URL, Token: readerToken, Branching: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Branches(t.Context()); !errors.Is(err, ErrBranchesDenied) {
		t.Errorf("Branches with a token that can't view them: %v, want ErrBranchesDenied", err)
	}
	if ok, err := c.CanRead(t.Context(), branchesPath); ok || err != nil {
		t.Errorf("CanRead(branches) = %v, %v; want false", ok, err)
	}
}
