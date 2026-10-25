package netboxtest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	if !strings.HasPrefix(path, "/api/") || !strings.HasSuffix(path, "/") || strings.ContainsAny(path, "?#") {
		return fmt.Errorf("POST %q: the path must be an API path such as /api/dcim/manufacturers/", path)
	}
	endpoint, err := localEndpoint(nb.URL, path)
	if err != nil {
		return err
	}
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("POST %s: encode the request: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("POST %s: %w", path, err)
	}
	req.Header.Set("Authorization", "Bearer "+nb.Token.Reveal())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	status, respBody, err := do(req)
	if err != nil {
		return fmt.Errorf("POST %s: %w", path, err)
	}
	switch {
	case status >= 400:
		return fmt.Errorf("POST %s: NetBox answered %d, not 201: %q", path, status, snippet(respBody))
	case status != http.StatusCreated:
		// Any other answer might hold a token, from /api/users/tokens/ say,
		// so don't quote it.
		return fmt.Errorf("POST %s: NetBox answered %d, not 201", path, status)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("POST %s: decode NetBox's answer: %w", path, err)
	}
	return nil
}

// UserToken provisions a read-only v2 token for another user of this NetBox,
// such as one a test made with Post to see what a token without some
// permission can do. password is the one the test gave that user.
func (nb *NetBox) UserToken(ctx context.Context, username, password string) (config.Secret, error) {
	return provisionToken(ctx, nb.URL, username, password, "netboxtest", false)
}
