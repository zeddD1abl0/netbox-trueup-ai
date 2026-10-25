package netbox

import (
	"context"
	"fmt"
	"slices"
	"sync"
)

// ObjectType is a NetBox model, such as dcim.devicetype, and the REST
// endpoint that lists it.
type ObjectType struct {
	Name     string
	Endpoint string
}

// readTypes are the object types milestone 1 reads (docs/roadmap.md):
// manufacturers, device types, and every component template that
// devicetype-library uses. Port mappings need no permission of their own: a
// front port template carries them, as rear_ports (FrontPortTemplateSerializer
// in netbox/dcim/api/serializers_/devicetype_components.py). The endpoints
// are those in netbox/dcim/api/urls.py.
var readTypes = []ObjectType{
	{Name: "dcim.manufacturer", Endpoint: "/api/dcim/manufacturers/"},
	{Name: "dcim.devicetype", Endpoint: "/api/dcim/device-types/"},
	{Name: "dcim.consoleporttemplate", Endpoint: "/api/dcim/console-port-templates/"},
	{Name: "dcim.consoleserverporttemplate", Endpoint: "/api/dcim/console-server-port-templates/"},
	{Name: "dcim.powerporttemplate", Endpoint: "/api/dcim/power-port-templates/"},
	{Name: "dcim.poweroutlettemplate", Endpoint: "/api/dcim/power-outlet-templates/"},
	{Name: "dcim.interfacetemplate", Endpoint: "/api/dcim/interface-templates/"},
	{Name: "dcim.frontporttemplate", Endpoint: "/api/dcim/front-port-templates/"},
	{Name: "dcim.rearporttemplate", Endpoint: "/api/dcim/rear-port-templates/"},
	{Name: "dcim.modulebaytemplate", Endpoint: "/api/dcim/module-bay-templates/"},
	{Name: "dcim.devicebaytemplate", Endpoint: "/api/dcim/device-bay-templates/"},
	{Name: "dcim.inventoryitemtemplate", Endpoint: "/api/dcim/inventory-item-templates/"},
}

// ReadTypes returns the object types TrueUp reads in milestone 1, in the
// order a sync would read them. The object model's specs (roadmap step 8)
// will take this list over.
func ReadTypes() []ObjectType {
	return slices.Clone(readTypes)
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
