package netboxtest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
)

// Post creates objects in this NetBox, for tests to seed it with. It sends
// body as JSON to path, such as /api/dcim/manufacturers/, with the
// superuser's token, and decodes NetBox's answer into out unless out is nil.
// body can be a list, which NetBox creates all at once or not at all.
//
// TrueUp's own NetBox client never writes, so tests seed NetBox through Post
// instead. Like the rest of netboxtest, it only talks to a NetBox on this
// machine.
func (nb *NetBox) Post(ctx context.Context, path string, body, out any) error {
	return nb.request(ctx, "", http.MethodPost, path, nil, body, out, http.StatusCreated)
}

// Patch changes an object in this NetBox, as a test needs: it sends the
// fields in body as JSON to the object's path, such as
// /api/dcim/device-types/7/, and decodes NetBox's answer into out unless out
// is nil.
func (nb *NetBox) Patch(ctx context.Context, path string, body, out any) error {
	return nb.request(ctx, "", http.MethodPatch, path, nil, body, out, http.StatusOK)
}

// Delete deletes an object from this NetBox, at its path, such as
// /api/dcim/device-types/7/.
func (nb *NetBox) Delete(ctx context.Context, path string) error {
	return nb.request(ctx, "", http.MethodDelete, path, nil, nil, nil, http.StatusNoContent)
}

// Get reads from this NetBox, as a test needs to see what something else
// made, such as NetBox's own import: it asks path, such as
// /api/dcim/interface-templates/, with query, such as device_type_id=7, and
// decodes NetBox's answer into out. TrueUp reads NetBox through its own
// client; this is for tests that check NetBox itself.
func (nb *NetBox) Get(ctx context.Context, path string, query url.Values, out any) error {
	return nb.request(ctx, "", http.MethodGet, path, query, nil, out, http.StatusOK)
}

// request sends a request to NetBox's API at path, with query, and body as
// JSON unless it is nil, in the branch whose schema ID is branch, or in main
// if branch is empty, and checks that NetBox answers with want.
func (nb *NetBox) request(ctx context.Context, branch, method, path string, query url.Values, body, out any, want int) error {
	// what names the request in errors, with its branch.
	what := method + " " + path
	if branch != "" {
		what += " in branch " + branch
	}
	if !strings.HasPrefix(path, "/api/") || !strings.HasSuffix(path, "/") || strings.ContainsAny(path, "?#") {
		return fmt.Errorf("%s %q: the path must be an API path such as /api/dcim/manufacturers/", method, path)
	}
	if branch != "" && !isSchemaID(branch) {
		return fmt.Errorf("%s: %q isn't a branch's schema ID", what, branch)
	}
	endpoint, err := localEndpoint(nb.URL, path)
	if err != nil {
		return err
	}
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("%s: encode the request: %w", what, err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	req.Header.Set("Authorization", "Bearer "+nb.Token.Reveal())
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if branch != "" {
		req.Header.Set(branchHeader, branch)
	}

	status, respBody, err := do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	switch {
	case status >= 400:
		return fmt.Errorf("%s: NetBox answered %d, not %d: %q", what, status, want, snippet(respBody))
	case status != want:
		// Any other answer might hold a token, from /api/users/tokens/ say,
		// so don't quote it.
		return fmt.Errorf("%s: NetBox answered %d, not %d", what, status, want)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("%s: decode NetBox's answer: %w", what, err)
	}
	return nil
}

// UserToken provisions a read-only v2 token for another user of this NetBox,
// such as one a test made with Post to see what a token without some
// permission can do. password is the one the test gave that user.
func (nb *NetBox) UserToken(ctx context.Context, username, password string) (config.Secret, error) {
	return provisionToken(ctx, nb.URL, username, password, "netboxtest", false)
}
