package netboxtest

import (
	"context"
	"fmt"
	"net/http"
)

// maxSchema caps the OpenAPI schema Schema reads. NetBox 4.7.2's is about
// 15 MB.
const maxSchema = 64 << 20

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
	status, body, err := doUpTo(req, maxSchema)
	switch {
	case err != nil:
		return nil, fmt.Errorf("GET /api/schema/: %w", err)
	case status != http.StatusOK:
		return nil, fmt.Errorf("GET /api/schema/: NetBox answered %d, not 200: %q", status, snippet(body))
	}
	return body, nil
}
