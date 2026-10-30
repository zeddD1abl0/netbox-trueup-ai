package netboxtest

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// maxSchema caps the OpenAPI schema Schema reads. NetBox 4.7.2's is about
// 15 MB.
const maxSchema = 64 << 20

// schemaClient is client with time for NetBox to generate its schema, which
// it does afresh for every request: about 4 seconds for NetBox 4.7.2 on a
// developer's machine, and more on a slow CI runner.
var schemaClient = func() *http.Client {
	c := *client
	c.Timeout = 2 * time.Minute
	return &c
}()

// Schema returns this NetBox's OpenAPI schema, in JSON, from /api/schema/.
// The spec packages' integration tests check their specs against it
// (internal/model/spectest).
func (nb *NetBox) Schema(ctx context.Context) ([]byte, error) {
	endpoint, err := localEndpoint(nb.URL, "/api/schema/")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?format=json", http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("GET /api/schema/: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+nb.Token.Reveal())
	req.Header.Set("Accept", "application/json")
	status, body, err := doUpTo(schemaClient, req, maxSchema)
	switch {
	case err != nil:
		return nil, fmt.Errorf("GET /api/schema/: %w", err)
	case status != http.StatusOK:
		return nil, fmt.Errorf("GET /api/schema/: NetBox answered %d, not 200: %q", status, snippet(body))
	}
	return body, nil
}
