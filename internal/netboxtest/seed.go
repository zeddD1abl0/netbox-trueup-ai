package netboxtest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	return nb.write(ctx, http.MethodPost, path, body, out, http.StatusCreated)
}

// Patch changes an object in this NetBox, as a test needs: it sends the
// fields in body as JSON to the object's path, such as
// /api/dcim/device-types/7/, and decodes NetBox's answer into out unless out
// is nil.
func (nb *NetBox) Patch(ctx context.Context, path string, body, out any) error {
	return nb.write(ctx, http.MethodPatch, path, body, out, http.StatusOK)
}

// Delete deletes an object from this NetBox, at its path, such as
// /api/dcim/device-types/7/.
func (nb *NetBox) Delete(ctx context.Context, path string) error {
	return nb.write(ctx, http.MethodDelete, path, nil, nil, http.StatusNoContent)
}

// write sends a request that changes NetBox, with body as JSON unless it is
// nil, and checks that NetBox answers with want.
func (nb *NetBox) write(ctx context.Context, method, path string, body, out any, want int) error {
	if !strings.HasPrefix(path, "/api/") || !strings.HasSuffix(path, "/") || strings.ContainsAny(path, "?#") {
		return fmt.Errorf("%s %q: the path must be an API path such as /api/dcim/manufacturers/", method, path)
	}
	endpoint, err := localEndpoint(nb.URL, path)
	if err != nil {
		return err
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("%s %s: encode the request: %w", method, path, err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+nb.Token.Reveal())
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	status, respBody, err := do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	switch {
	case status >= 400:
		return fmt.Errorf("%s %s: NetBox answered %d, not %d: %q", method, path, status, want, snippet(respBody))
	case status != want:
		// Any other answer might hold a token, from /api/users/tokens/ say,
		// so don't quote it.
		return fmt.Errorf("%s %s: NetBox answered %d, not %d", method, path, status, want)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("%s %s: decode NetBox's answer: %w", method, path, err)
	}
	return nil
}

// UserToken provisions a read-only v2 token for another user of this NetBox,
// such as one a test made with Post to see what a token without some
// permission can do. password is the one the test gave that user.
func (nb *NetBox) UserToken(ctx context.Context, username, password string) (config.Secret, error) {
	return provisionToken(ctx, nb.URL, username, password, "netboxtest", false)
}
