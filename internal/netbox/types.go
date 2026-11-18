package netbox

import (
	"context"
	"fmt"
	"slices"
	"sync"
)

// ObjectType is a NetBox model, such as dcim.devicetype, and the REST
// endpoint that lists it. The object model's specs (internal/model) say
// which TrueUp reads, and where.
type ObjectType struct {
	Name     string
	Endpoint string
}

// ReadCheck says whether the token may read one object type.
type ReadCheck struct {
	Type     ObjectType
	Readable bool
}

// CheckRead checks, for each type, whether the token may read it (see
// CanRead), and returns the results in the same order. If any can't be read,
// it checks the token again, so that a token that stopped working partway
// through, by expiring say, is reported as refused, not as lacking
// permissions. If any type fails, its error is returned, saying how many
// others failed too, instead of the results.
func (c *Client) CheckRead(ctx context.Context, types []ObjectType) ([]ReadCheck, error) {
	results := make([]ReadCheck, len(types))
	errs := make([]error, len(types))
	var wg sync.WaitGroup
	for i, t := range types {
		// The client's slots cap how many of these are in flight.
		wg.Go(func() {
			ok, err := c.CanRead(ctx, t.Endpoint)
			results[i], errs[i] = ReadCheck{Type: t, Readable: ok}, err
		})
	}
	wg.Wait()
	var first error
	failed := 0
	for _, err := range errs {
		if err != nil {
			if first == nil {
				first = err
			}
			failed++
		}
	}
	switch {
	case failed > 2:
		return nil, fmt.Errorf("%w (and %d other object types failed too)", first, failed-1)
	case failed == 2:
		return nil, fmt.Errorf("%w (and 1 other object type failed too)", first)
	case first != nil:
		return nil, first
	}
	if slices.ContainsFunc(results, func(r ReadCheck) bool { return !r.Readable }) {
		if _, err := c.checkToken(ctx); err != nil {
			return nil, err
		}
	}
	return results, nil
}
