package netbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"
)

// SupportedBranching is the netbox-branching release line TrueUp works with:
// 1.2.x, the line for NetBox 4.7 (COMPATIBILITY.md in netbox-branching, and
// min_version and max_version in netbox_branching/__init__.py, 1.2.4).
const SupportedBranching = "1.2"

// Branch statuses that TrueUp tells apart. netbox-branching has eleven
// (BranchStatusChoices in netbox_branching/choices.py, 1.2.4), and only a
// ready branch can be used: its middleware refuses a request in any other.
const BranchReady = "ready"

const (
	// branchingPlugin is netbox-branching's name in NetBox's list of
	// installed plugins, /api/status/'s plugins.
	branchingPlugin = "netbox_branching"
	// branchHeader names the branch a request works in, by its schema ID
	// (BRANCH_HEADER in netbox_branching/constants.py).
	branchHeader = "X-NetBox-Branch"
	// branchesPath is netbox-branching's list of branches.
	branchesPath = "/api/plugins/branching/branches/"
)

// schemaIDPattern is what netbox-branching makes a branch's schema ID of:
// eight lower case letters and digits (Branch._generate_schema_id in
// netbox_branching/models/branches.py). It goes in a header, so the client
// checks NetBox's answer before sending it.
var schemaIDPattern = regexp.MustCompile(`^[a-z0-9]{8}$`)

// branchingPattern matches the versions NetBox reports for a plugin: its
// release, such as 1.2.4, then maybe a designation, such as a PEP 440
// pre-release (1.2.5b1) or the -<release_track> NetBox adds
// (get_installed_plugins in netbox/netbox/plugins/utils.py).
var branchingPattern = regexp.MustCompile(`^([0-9]{1,4})\.([0-9]{1,4})\.[0-9]{1,4}([-+.]?[0-9A-Za-z][0-9A-Za-z.+-]{0,63})?$`)

// supportedBranching reports whether a netbox-branching version is of the
// release line TrueUp works with.
func supportedBranching(version string) bool {
	m := branchingPattern.FindStringSubmatch(version)
	return m != nil && m[1]+"."+m[2] == SupportedBranching
}

// branchingRelease names the netbox-branching NetBox reported, for a
// message: its version as given when it looks like one, quoted otherwise.
func branchingRelease(version string) string {
	switch {
	case version == "":
		return "netbox-branching, without saying which release"
	case branchingPattern.MatchString(version):
		return "netbox-branching " + version
	default:
		return "netbox-branching " + strconv.Quote(version)
	}
}

// BranchingError is returned by Connect when netbox.branching is on but NetBox
// hasn't got the netbox-branching TrueUp works with (SupportedBranching), or
// doesn't say which it has.
type BranchingError struct {
	// Unlisted says NetBox's status doesn't list its plugins at all.
	Unlisted bool
	// Installed says NetBox lists netbox-branching, and Version is the version
	// it gave, cut short, or empty if it gave none.
	Installed bool
	Version   string
}

func (e *BranchingError) Error() string {
	const line = "TrueUp works with netbox-branching " + SupportedBranching + ".x only, the release line for NetBox " + SupportedVersion
	switch {
	case e.Unlisted:
		return "netbox.branching is on, but NetBox's status doesn't list its plugins, so TrueUp can't tell whether it has netbox-branching: " + line
	case !e.Installed:
		return "netbox.branching is on, but NetBox doesn't have netbox-branching: install netbox-branching " + SupportedBranching +
			".x, the release line for NetBox " + SupportedVersion + ", or turn netbox.branching off"
	default:
		return "netbox.branching is on, but NetBox has " + branchingRelease(e.Version) + ": " + line
	}
}

// plugins is what /api/status/ says of NetBox's installed plugins
// (StatusView in netbox/netbox/api/views.py): a JSON object of each plugin's
// name to its version, or null.
type plugins struct {
	// listed says the answer has a plugins object.
	listed bool
	// branching says it lists netbox-branching, and branchingVersion is the
	// version it gave, cut and with the token removed, or empty for null.
	branching        bool
	branchingVersion string
}

// readPlugins reads /api/status/'s plugins. Absent, it says nothing: a NetBox
// that hides them, as a hosted one might, is no fault while nothing needs
// them. Anything but an object, or a version that is neither text nor null,
// is an error.
func (c *Client) readPlugins(raw json.RawMessage) (plugins, error) {
	if len(raw) == 0 {
		return plugins{}, nil
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(raw, &all); err != nil || all == nil {
		return plugins{}, fmt.Errorf("GET %s: the answer's plugins aren't a JSON object of each plugin's version", statusPath)
	}
	p := plugins{listed: true}
	v, ok := all[branchingPlugin]
	if !ok {
		return p, nil
	}
	p.branching = true
	if string(v) == "null" {
		return p, nil
	}
	var version string
	if err := json.Unmarshal(v, &version); err != nil {
		return plugins{}, fmt.Errorf("GET %s: the answer gives %s's version as neither text nor null", statusPath, branchingPlugin)
	}
	p.branchingVersion = cut(c.redact(version))
	return p, nil
}

// checkBranching checks the plugins NetBox lists against netbox.branching:
// on, NetBox must have the netbox-branching TrueUp works with; off, NetBox
// having netbox-branching at all is worth a warning, since branches are
// there to be used. It returns the warning, if any.
func (c *Client) checkBranching(p plugins) (warning string, err error) {
	switch {
	case c.branching && !p.listed:
		return "", &BranchingError{Unlisted: true}
	case c.branching && (!p.branching || !supportedBranching(p.branchingVersion)):
		return "", &BranchingError{Installed: p.branching, Version: p.branchingVersion}
	case c.branching, !p.branching:
		return "", nil
	case supportedBranching(p.branchingVersion):
		return "NetBox has " + branchingRelease(p.branchingVersion) +
			", but netbox.branching is off, so TrueUp reads and writes main only: turn netbox.branching on to work in branches", nil
	default:
		return "NetBox has " + branchingRelease(p.branchingVersion) + ", which TrueUp doesn't work with (it works with " +
			SupportedBranching + ".x only, the release line for NetBox " + SupportedVersion +
			"), and netbox.branching is off, so TrueUp reads and writes main only", nil
	}
}

// Branch is one of NetBox's branches, from netbox-branching.
type Branch struct {
	// ID is the branch's ID in NetBox.
	ID int64
	// Name is the branch's name, unique among NetBox's branches.
	Name string
	// SchemaID identifies the branch in the X-NetBox-Branch header: eight
	// lower case letters and digits. Unlike the name, it never changes.
	SchemaID string
	// Status is the branch's status, such as ready or merging, as NetBox
	// gave it. Only a ready branch can be used (Ready).
	Status string
	// Description is the branch's description, or empty.
	Description string
	// Owner is the user name of the branch's owner, or empty if it has none.
	Owner string
	// LastSync is when the branch was last synchronized with main, or was
	// provisioned, or zero if NetBox gave no time.
	LastSync time.Time
}

// Ready reports whether the branch can be used: netbox-branching's
// middleware refuses a request in a branch that isn't ready
// (get_active_branch in netbox_branching/utilities.py).
func (b Branch) Ready() bool { return b.Status == BranchReady }

// branchJSON is the part of netbox-branching's answer about a branch that
// the client reads (BranchSerializer in netbox_branching/api/serializers.py).
type branchJSON struct {
	Name     *string `json:"name"`
	SchemaID *string `json:"schema_id"`
	Status   *struct {
		Value *string `json:"value"`
	} `json:"status"`
	Description *string `json:"description"`
	Owner       *struct {
		Username *string `json:"username"`
	} `json:"owner"`
	LastSync *string `json:"last_sync"`
}

// readBranch reads one branch of a listing, whose id List has checked.
func (c *Client) readBranch(raw json.RawMessage) (Branch, error) {
	id, err := objectID(raw)
	if err != nil {
		return Branch{}, err
	}
	var b branchJSON
	if err := json.Unmarshal(raw, &b); err != nil {
		return Branch{}, fmt.Errorf("branch #%d can't be read: %w", id, err)
	}
	switch {
	case b.Name == nil || *b.Name == "":
		return Branch{}, fmt.Errorf("NetBox sent branch #%d without a name", id)
	case b.SchemaID == nil || !schemaIDPattern.MatchString(*b.SchemaID):
		return Branch{}, fmt.Errorf("NetBox sent branch #%d without a schema ID of eight lower case letters and digits", id)
	case b.Status == nil || b.Status.Value == nil || *b.Status.Value == "":
		return Branch{}, fmt.Errorf("NetBox sent branch #%d without a status", id)
	}
	branch := Branch{
		ID: id, Name: cut(c.redact(*b.Name)), SchemaID: *b.SchemaID, Status: cut(c.redact(*b.Status.Value)),
	}
	if b.Description != nil {
		branch.Description = cut(c.redact(*b.Description))
	}
	if b.Owner != nil && b.Owner.Username != nil {
		branch.Owner = cut(c.redact(*b.Owner.Username))
	}
	if b.LastSync != nil {
		t, err := time.Parse(time.RFC3339Nano, *b.LastSync)
		if err != nil {
			return Branch{}, fmt.Errorf("NetBox sent branch #%d with a last_sync that isn't an RFC 3339 time", id)
		}
		branch.LastSync = t.UTC()
	}
	return branch, nil
}

// ErrBranchesDenied is returned, wrapped, by Branches and FindBranch when
// NetBox refuses the token the list of branches: its user lacks the view
// permission on netbox_branching.branch.
var ErrBranchesDenied = errors.New("the token can't view NetBox's branches: grant its user the view permission on netbox_branching.branch")

// Branches returns NetBox's branches, in ID order, as netbox-branching lists
// them, whatever their status. It never sends the branch header, even from
// a client bound to a branch (InBranch): netbox-branching refuses every
// request in a branch that isn't ready, its own list included.
//
// The list ignores brief and fields, and object permissions' constraints
// don't filter it, since netbox-branching's BranchViewSet is REST framework's
// own ModelViewSet rather than NetBox's (netbox_branching/api/views.py).
func (c *Client) Branches(ctx context.Context) ([]Branch, error) {
	return c.listBranches(ctx, nil, "")
}

// ErrNoBranch is returned, wrapped, by FindBranch when NetBox has no branch
// of the name given.
var ErrNoBranch = errors.New("NetBox has no branch of that name")

// FindBranch returns the branch named name, exactly, case included, whatever
// its status. It asks NetBox for the branch by name, without the branch
// header, and checks the name itself, since NetBox ignores a filter it
// doesn't know. It returns an error wrapping ErrNoBranch if NetBox has none.
func (c *Client) FindBranch(ctx context.Context, name string) (Branch, error) {
	if name == "" {
		return Branch{}, errors.New("a branch's name can't be empty")
	}
	found, err := c.listBranches(ctx, url.Values{"name": {name}}, name)
	switch {
	case err != nil:
		return Branch{}, err
	case len(found) == 0:
		return Branch{}, fmt.Errorf("%w: %q", ErrNoBranch, name)
	case len(found) > 1:
		// Branch.name is unique (netbox_branching/models/branches.py).
		return Branch{}, fmt.Errorf("GET %s: NetBox sent %d branches named %q, though names are unique", branchesPath, len(found), name)
	}
	return found[0], nil
}

// listBranches lists the branches that query's filters match, without the
// branch header, keeping only those named name if it isn't empty.
func (c *Client) listBranches(ctx context.Context, query url.Values, name string) ([]Branch, error) {
	main := c.onMain()
	var out []Branch
	err := main.List(ctx, branchesPath, query, func(raw json.RawMessage) error {
		b, err := main.readBranch(raw)
		if err != nil {
			return fmt.Errorf("GET %s: %w", branchesPath, err)
		}
		if name == "" || b.Name == name {
			out = append(out, b)
		}
		return nil
	})
	if err != nil {
		if refusedByNetBox(err) {
			return nil, fmt.Errorf("%w: %w", ErrBranchesDenied, err)
		}
		return nil, err
	}
	return out, nil
}

// InBranch returns a client that reads from branch b of the same NetBox,
// sending the X-NetBox-Branch header on every request but those that read
// branches themselves (Branches and FindBranch). It shares the receiver's
// connections and its limit on requests in flight. b is usually what
// FindBranch returned; InBranch doesn't check that it is ready, and NetBox
// refuses each request, with a *BranchRefusedError, while it isn't.
func (c *Client) InBranch(b Branch) (*Client, error) {
	if !schemaIDPattern.MatchString(b.SchemaID) {
		return nil, fmt.Errorf("branch %q: its schema ID isn't eight lower case letters and digits", b.Name)
	}
	bound := *c
	bound.branch = &b
	bound.info.Branch = &b
	bound.info.Warnings = c.Info().Warnings
	return &bound, nil
}

// onMain returns the client itself, or if it is bound to a branch, a client
// on main that shares its connections and limits.
func (c *Client) onMain() *Client {
	if c.branch == nil {
		return c
	}
	main := *c
	main.branch = nil
	main.info.Branch = nil
	return &main
}

// ErrBranchRefused is wrapped by the *BranchRefusedError that a client bound
// to a branch returns when NetBox refuses the branch.
var ErrBranchRefused = errors.New("NetBox refused the branch")

// BranchRefusedError is netbox-branching refusing a request in a branch: one
// that no longer exists ("Invalid branch identifier"), or that isn't ready
// ("Selected branch is not ready."), such as one being synchronized, merged
// or migrated. Its middleware answers 400 with plain text, before REST
// framework or the token is involved (BranchMiddleware in
// netbox_branching/middleware.py); NetBox's own middleware, around it, adds
// the API-Version header (CoreMiddleware in netbox/netbox/middleware.py).
type BranchRefusedError struct {
	// Path is the request's path.
	Path string
	// Branch is the branch the request named.
	Branch Branch
	// Message is NetBox's answer, cut short, with the token removed. It
	// comes from the server, so print it quoted, as Error does.
	Message string
}

func (e *BranchRefusedError) Error() string {
	return fmt.Sprintf("GET %s: NetBox refused branch %q (schema ID %s): %q; it was ready when TrueUp chose it, "+
		"so someone may be synchronizing, merging or migrating it, or have deleted it", e.Path, e.Branch.Name, e.Branch.SchemaID, e.Message)
}

func (e *BranchRefusedError) Unwrap() error { return ErrBranchRefused }

// branchRefusal returns the error for an answer that is netbox-branching
// refusing the request's branch, or nil for any other: a 400 that NetBox
// gave, since it carries API-Version, that isn't JSON, since REST
// framework's own 400s are, to a request that named a branch. NetBox's text
// isn't matched: one of the two is translated, and either could change.
func (c *Client) branchRefusal(path string, ans answer) error {
	if c.branch == nil || ans.status != http.StatusBadRequest || ans.header.Get("API-Version") == "" || isJSON(ans.header.Get("Content-Type")) {
		return nil
	}
	return &BranchRefusedError{Path: path, Branch: *c.branch, Message: cut(c.redact(string(ans.body)))}
}
