package netboxtest

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// branchHeader names the branch a request works in, by its schema ID.
const branchHeader = "X-NetBox-Branch"

// branchesPath is netbox-branching's list of branches.
const branchesPath = "/api/plugins/branching/branches/"

// schemaID is what netbox-branching makes a branch's schema ID of: eight
// lower case letters and digits (Branch._generate_schema_id in
// netbox_branching/models/branches.py, 1.2.4). It goes in a header, so
// netboxtest checks NetBox's answer before sending it.
var schemaID = regexp.MustCompile(`^[a-z0-9]{8}$`)

func isSchemaID(s string) bool { return schemaID.MatchString(s) }

// branchPoll is how often CreateBranch reads a new branch's status. A branch
// of a test NetBox is ready in a second or two.
const branchPoll = 250 * time.Millisecond

// branchWait is how long CreateBranch waits for a branch to be ready, at most,
// so that a provision that never ends fails the test that asked for it, with
// its job's state, rather than every test in the binary at go test's timeout.
const branchWait = 2 * time.Minute

// mainOnlyPaths are API paths that netbox-branching always serves from main,
// whatever branch a request names: /api/status/ (EXEMPT_PATHS in
// netbox_branching/constants.py, 1.2.4); the objects of the models it exempts
// (EXEMPT_MODELS there): core's, its own, and extras' custom fields, custom
// links, event rules, export templates, notification groups, saved filters and
// webhooks; and users, tokens and permissions, which aren't change-logged
// (supports_branching in utilities.py). BranchClient refuses them, rather
// than write in main what a test meant for a branch. A few more of extras'
// models aren't change-logged either; /api/plugins/branching/branchable-models/
// lists every model a branch holds.
var mainOnlyPaths = []string{
	"/api/status/",
	"/api/users/",
	"/api/core/",
	"/api/plugins/branching/",
	"/api/extras/custom-fields/",
	"/api/extras/custom-field-choice-sets/",
	"/api/extras/custom-links/",
	"/api/extras/event-rules/",
	"/api/extras/export-templates/",
	"/api/extras/notification-groups/",
	"/api/extras/saved-filters/",
	"/api/extras/webhooks/",
}

// Branch is a branch of this NetBox, from netbox-branching.
type Branch struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	// SchemaID identifies the branch in the X-NetBox-Branch header.
	SchemaID string `json:"schema_id"`
}

// branchState is the part of netbox-branching's answer about a branch that
// CreateBranch reads.
type branchState struct {
	Branch
	Status struct {
		Value string `json:"value"`
	} `json:"status"`
}

// CreateBranch creates a branch of this NetBox with netbox-branching, as a
// test needs one to work in, and returns once the branch is ready. NetBox's
// worker provisions it, copying main, in a background job. CreateBranch fails
// if the branch's provisioning fails, with the job's error; if the worker
// stops, with the end of its log; or when ctx ends.
//
// It works only in the Branching profile. TrueUp's own client finds and uses
// branches through internal/netbox.
func (nb *NetBox) CreateBranch(ctx context.Context, name, description string) (Branch, error) {
	return nb.createBranch(ctx, name, description, nb.checkWorker)
}

// createBranch creates a branch, checking with alive that the worker, which
// provisions it, runs: first, so that a stopped worker leaves no branch that
// nothing will provision, and then while it waits.
func (nb *NetBox) createBranch(ctx context.Context, name, description string, alive func(context.Context) error) (Branch, error) {
	if nb.Profile != Branching {
		return Branch{}, fmt.Errorf("create branch %q: this NetBox runs the %s profile, which has no netbox-branching; run the tests with %s=%s", name, nb.Profile, ProfileVariable, Branching)
	}
	if err := alive(ctx); err != nil {
		return Branch{}, fmt.Errorf("create branch %q: %w", name, err)
	}
	var b branchState
	if err := nb.Post(ctx, branchesPath, map[string]string{"name": name, "description": description}, &b); err != nil {
		return Branch{}, fmt.Errorf("create branch %q: %w", name, err)
	}
	// From here on the branch exists, and stays in this disposable NetBox if
	// CreateBranch fails: deleting it while its job may still run would race
	// the worker.
	if b.ID <= 0 || !isSchemaID(b.SchemaID) {
		return Branch{}, fmt.Errorf("create branch %q: NetBox made it, but its answer has no usable ID and schema ID", name)
	}
	if err := nb.waitForBranch(ctx, b, alive, branchWait); err != nil {
		return Branch{}, fmt.Errorf("create branch %q (ID %d, left in NetBox): %w", name, b.ID, err)
	}
	return b.Branch, nil
}

// waitForBranch waits until a new branch is ready, for limit at most,
// checking with alive before each look that the worker, which provisions it,
// still runs.
func (nb *NetBox) waitForBranch(ctx context.Context, b branchState, alive func(context.Context) error, limit time.Duration) error {
	waitCtx, cancel := context.WithTimeoutCause(ctx, limit, fmt.Errorf("it wasn't ready within %v", limit))
	defer cancel()
	tick := time.NewTicker(branchPoll)
	defer tick.Stop()
	path := branchesPath + strconv.Itoa(b.ID) + "/"
	status := b.Status.Value
	// still reports a branch the wait ran out on, with what NetBox's job for
	// it says, asked with a context of its own, since waitCtx has ended.
	still := func() error {
		reportCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		return fmt.Errorf("the branch is still %s: %w%s", status, context.Cause(waitCtx), nb.branchJobReport(reportCtx, b.ID))
	}
	for {
		// Without a worker, the branch would wait for ever.
		if err := alive(waitCtx); err != nil {
			// The check fails too once the wait ends, which isn't the worker's fault.
			if waitCtx.Err() != nil {
				return still()
			}
			return err
		}
		var st branchState
		if err := nb.Get(waitCtx, path, nil, &st); err != nil {
			if waitCtx.Err() != nil {
				return still()
			}
			return err
		}
		switch status = st.Status.Value; status {
		case "ready":
			return nil
		case "new", "provisioning":
		case "":
			return errors.New("NetBox's answer about the branch has no status")
		case "failed":
			return fmt.Errorf("provisioning the branch failed%s", nb.branchJobReport(ctx, b.ID))
		default:
			return fmt.Errorf("the branch is %q, not provisioning or ready", status)
		}
		select {
		case <-waitCtx.Done():
			return still()
		case <-tick.C:
		}
	}
}

// checkWorker fails if NetBox's worker has stopped, with its last health
// checks and the end of its log.
func (nb *NetBox) checkWorker(ctx context.Context) error {
	worker := nb.container(workerService)
	if worker == nil {
		return fmt.Errorf("this NetBox has no %s service to run branch jobs", workerService)
	}
	info, err := worker.Inspect(ctx)
	if err != nil {
		return fmt.Errorf("inspect NetBox's worker: %w", err)
	}
	if info.State == nil || !info.State.Running {
		return fmt.Errorf("NetBox's worker, which provisions branches, has stopped%s", diagnose(ctx, worker))
	}
	return nil
}

// branchJobReport returns what NetBox recorded about a branch's latest job,
// such as the error that made provisioning fail, to append to an error. It
// reports why it can't instead.
func (nb *NetBox) branchJobReport(ctx context.Context, branchID int) string {
	var jobs struct {
		Results []struct {
			Name   string `json:"name"`
			Status struct {
				Value string `json:"value"`
			} `json:"status"`
			Error string `json:"error"`
		} `json:"results"`
	}
	query := url.Values{
		"object_type": {"netbox_branching.branch"},
		"object_id":   {strconv.Itoa(branchID)},
		"ordering":    {"-created"},
		"limit":       {"1"},
	}
	if err := nb.Get(ctx, "/api/core/jobs/", query, &jobs); err != nil {
		return fmt.Sprintf("\n(its job can't be read: %v)", err)
	}
	if len(jobs.Results) == 0 {
		return "\n(NetBox has no job for it)"
	}
	j := jobs.Results[0]
	return fmt.Sprintf("\nIts job %q is %q, with the error %q.", j.Name, j.Status.Value, j.Error)
}

// BranchClient sends requests to one branch of a NetBox. Its methods work as
// NetBox's own Post, Patch, Delete and Get do, with the X-NetBox-Branch header,
// so that what they change and read is the branch's, not main's. netbox-branching
// branches only some models: the client refuses the API paths it always serves
// from main, but some of extras' models, such as custom fields and webhooks,
// are main's even through it.
type BranchClient struct {
	nb     *NetBox
	branch string
}

// InBranch returns a client for branch b of this NetBox, such as one
// CreateBranch made.
func (nb *NetBox) InBranch(b Branch) *BranchClient {
	return &BranchClient{nb: nb, branch: b.SchemaID}
}

// Post creates objects in the branch, as NetBox's Post does in main.
func (c *BranchClient) Post(ctx context.Context, path string, body, out any) error {
	return c.request(ctx, http.MethodPost, path, nil, body, out, http.StatusCreated)
}

// Patch changes an object in the branch, as NetBox's Patch does in main.
func (c *BranchClient) Patch(ctx context.Context, path string, body, out any) error {
	return c.request(ctx, http.MethodPatch, path, nil, body, out, http.StatusOK)
}

// Delete deletes an object from the branch, as NetBox's Delete does in main.
func (c *BranchClient) Delete(ctx context.Context, path string) error {
	return c.request(ctx, http.MethodDelete, path, nil, nil, nil, http.StatusNoContent)
}

// Get reads from the branch, as NetBox's Get does from main.
func (c *BranchClient) Get(ctx context.Context, path string, query url.Values, out any) error {
	return c.request(ctx, http.MethodGet, path, query, nil, out, http.StatusOK)
}

// request sends a request in the client's branch. A client without one,
// such as one made from a zero Branch, refuses, since an empty branch would
// send the request to main.
func (c *BranchClient) request(ctx context.Context, method, path string, query url.Values, body, out any, want int) error {
	if c.branch == "" {
		return fmt.Errorf("%s %s: no branch to send it in; make the client with InBranch, from a Branch that CreateBranch returned", method, path)
	}
	for _, p := range mainOnlyPaths {
		if strings.HasPrefix(path, p) {
			return fmt.Errorf("%s %s: netbox-branching serves %s from main, whatever the branch; use NetBox's own methods", method, path, p)
		}
	}
	return c.nb.request(ctx, c.branch, method, path, query, body, out, want)
}
