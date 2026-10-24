package netbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// page is one page of a NetBox listing (NetBoxPagination in
// netbox/netbox/api/pagination.py). In cursor mode count and previous are
// always null, so only next and results matter. next is kept raw, to tell a
// missing link from a null one.
type page struct {
	Next    json.RawMessage    `json:"next"`
	Results *[]json.RawMessage `json:"results"`
}

// hasNext reports whether the page links to another, or fails if its next
// field is missing or isn't a link or null.
func (p page) hasNext() (bool, error) {
	switch {
	case len(p.Next) == 0:
		return false, errors.New("the answer has no next field")
	case string(p.Next) == "null":
		return false, nil
	case p.Next[0] == '"':
		return true, nil
	default:
		return false, errors.New("the answer's next field is neither a link nor null")
	}
}

// List calls fn with each object at endpoint, such as
// /api/dcim/manufacturers/, that query's filters match, in ID order. If fn
// returns an error, List stops and returns it.
//
// List pages with NetBox's cursor pagination: each page asks for objects
// from the ID after the last one it got (?start=), which stays right while
// objects are added or removed, and spares NetBox counting them. NetBox
// returns objects with IDs from start, ordered by ID, and refuses start
// with offset or ordering, so query must not set those, nor start or limit.
// NetBox may give fewer objects than asked for when its MAX_PAGE_SIZE is
// lower, so List follows the next link's presence, not page sizes, and
// checks that IDs only go up. NetBox links to a next page whenever a page is
// full, so a full page without a link, which would end the listing early, is
// refused too.
func (c *Client) List(ctx context.Context, endpoint string, query url.Values, fn func(json.RawMessage) error) error {
	if err := checkListing(endpoint, query); err != nil {
		return err
	}
	return c.list(ctx, endpoint, query, fn)
}

func (c *Client) list(ctx context.Context, endpoint string, query url.Values, fn func(json.RawMessage) error) error {
	q := maps.Clone(query)
	if q == nil {
		q = url.Values{}
	}
	q.Set("limit", strconv.Itoa(c.pageSize))
	var start int64 // the first ID the next page may hold
	for {
		q.Set("start", strconv.FormatInt(start, 10))
		var p page
		if _, err := c.getJSON(ctx, endpoint, q, &p); err != nil {
			return err
		}
		if p.Results == nil {
			return fmt.Errorf("GET %s: the answer has no results list", endpoint)
		}
		results := *p.Results
		if len(results) > c.pageSize {
			return fmt.Errorf("GET %s: NetBox sent %d objects, more than the %d asked for", endpoint, len(results), c.pageSize)
		}
		last := start - 1
		for _, raw := range results {
			id, err := objectID(raw)
			if err != nil {
				return fmt.Errorf("GET %s: %w", endpoint, err)
			}
			if id <= last {
				return fmt.Errorf("GET %s: NetBox sent object %d after %d, out of the ID order cursor pagination gives", endpoint, id, last)
			}
			last = id
			if err := fn(raw); err != nil {
				return err
			}
		}
		next, err := p.hasNext()
		if err != nil {
			return fmt.Errorf("GET %s: %w", endpoint, err)
		}
		if !next {
			if len(results) == c.pageSize {
				return fmt.Errorf("GET %s: NetBox sent a full page of %d objects without a link to the next, which would end the listing early", endpoint, len(results))
			}
			return nil
		}
		if len(results) == 0 {
			return fmt.Errorf("GET %s: NetBox sent an empty page that links to another", endpoint)
		}
		if last == math.MaxInt64 {
			return fmt.Errorf("GET %s: NetBox links to a page after the largest possible ID", endpoint)
		}
		start = last + 1
	}
}

// objectID returns the id of an object in a listing.
func objectID(raw json.RawMessage) (int64, error) {
	var obj struct {
		ID *int64 `json:"id"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return 0, fmt.Errorf("an object in the answer can't be read: %w", err)
	}
	if obj.ID == nil || *obj.ID <= 0 {
		return 0, errors.New("NetBox sent an object without a positive id")
	}
	return *obj.ID, nil
}

// parentField matches the names of fields that refer to a parent, such as
// device_type.
var parentField = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// ListByParent calls fn with each object at endpoint whose parent is one of
// parents, and that query's filters match. For example, it lists the
// interface templates of many device types, with endpoint
// /api/dcim/interface-templates/ and parent device_type. If fn returns an
// error, ListByParent stops and returns it.
//
// It filters with NetBox's <parent>_id filter (DeviceTypeComponentFilterSet
// in netbox/dcim/filtersets.py, for example), sending as many IDs in each
// request as keep the URL short enough for common web servers, and lists
// each batch as List does: in ID order within a batch, but not across them.
//
// NetBox ignores a filter it doesn't know, and would then list every object,
// so ListByParent checks that each object's parent field refers to one of
// the IDs it asked for, and fails otherwise. query must therefore not set
// brief, whose objects leave the parent out, and its fields or omit must
// keep it. NetBox refuses the whole request, with 400, if one of the IDs
// refers to nothing, such as a device type deleted since it was listed.
func (c *Client) ListByParent(ctx context.Context, endpoint, parent string, parents []int64, query url.Values, fn func(json.RawMessage) error) error {
	if err := checkListing(endpoint, query); err != nil {
		return err
	}
	filter := parent + "_id"
	if !parentField.MatchString(parent) {
		return fmt.Errorf("list %s: %q isn't a field name", endpoint, parent)
	}
	if query.Has(filter) {
		return fmt.Errorf("list %s: query must not set %s, which ListByParent sets", endpoint, filter)
	}
	if query.Has("brief") {
		return fmt.Errorf("list %s: query must not set brief, whose objects leave out %s", endpoint, parent)
	}
	ids := slices.Compact(slices.Sorted(slices.Values(parents)))
	if len(ids) > 0 && ids[0] <= 0 {
		return fmt.Errorf("list %s: parent IDs must be positive, not %d", endpoint, ids[0])
	}
	for _, batch := range batches(filter, ids, c.maxFilterBytes) {
		want := make(map[int64]bool, len(batch))
		q := maps.Clone(query)
		if q == nil {
			q = url.Values{}
		}
		for _, id := range batch {
			want[id] = true
			q.Add(filter, strconv.FormatInt(id, 10))
		}
		err := c.list(ctx, endpoint, q, func(raw json.RawMessage) error {
			id, err := parentID(raw, parent)
			if err != nil {
				return fmt.Errorf("GET %s: %w", endpoint, err)
			}
			if !want[id] {
				return fmt.Errorf("GET %s: NetBox sent an object whose %s is %d, which wasn't asked for: is %s a filter of %s?", endpoint, parent, id, filter, endpoint)
			}
			return fn(raw)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// batches splits ids so that each batch, as filter=id&filter=id..., takes up
// at most limit bytes, with at least one ID in each.
func batches(filter string, ids []int64, limit int) [][]int64 {
	var out [][]int64
	var batch []int64
	size := 0
	for _, id := range ids {
		n := len(filter) + len("=&") + len(strconv.FormatInt(id, 10))
		if len(batch) > 0 && size+n > limit {
			out = append(out, batch)
			batch, size = nil, 0
		}
		batch = append(batch, id)
		size += n
	}
	if len(batch) > 0 {
		out = append(out, batch)
	}
	return out
}

// parentID returns the ID of the object that an object's field refers to.
// NetBox gives a reference as a nested object with an id.
func parentID(raw json.RawMessage, field string) (int64, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return 0, fmt.Errorf("an object in the answer can't be read: %w", err)
	}
	ref, ok := obj[field]
	if !ok || string(ref) == "null" {
		return 0, fmt.Errorf("NetBox sent an object without %s: if the query sets fields or omit, keep %s", field, field)
	}
	var nested struct {
		ID *int64 `json:"id"`
	}
	if err := json.Unmarshal(ref, &nested); err != nil || nested.ID == nil {
		return 0, fmt.Errorf("NetBox sent an object whose %s has no id", field)
	}
	return *nested.ID, nil
}

// checkListing checks a listing's arguments, which come from TrueUp's own
// code: endpoint must be an API path ending in a slash, and query must not set
// what the client controls.
func checkListing(endpoint string, query url.Values) error {
	if !strings.HasPrefix(endpoint, "/api/") || !strings.HasSuffix(endpoint, "/") || strings.ContainsAny(endpoint, "?#") {
		return fmt.Errorf("list %q: an endpoint is an API path such as /api/dcim/manufacturers/", endpoint)
	}
	for _, key := range []string{"start", "limit", "offset", "ordering"} {
		if query.Has(key) {
			return fmt.Errorf("list %s: query must not set %s, which pagination controls", endpoint, key)
		}
	}
	return nil
}

// CanRead reports whether the token may read the objects at endpoint: true
// if NetBox lists them, and false if it answers 403. That is how NetBox
// answers a user without the view permission on the object type: REST
// framework's DjangoModelPermissions, which TokenPermissions in
// netbox/netbox/api/authentication.py extends, checks it before any object
// is read. A view permission with constraints counts as one, although the
// token then sees only some of the objects.
func (c *Client) CanRead(ctx context.Context, endpoint string) (bool, error) {
	if err := checkListing(endpoint, nil); err != nil {
		return false, err
	}
	q := url.Values{"brief": {"true"}, "limit": {"1"}, "start": {"0"}}
	var p page
	if _, err := c.getJSON(ctx, endpoint, q, &p); err != nil {
		// A 403 from a proxy or firewall says nothing about permissions.
		if refusedByNetBox(err) {
			return false, nil
		}
		return false, err
	}
	if p.Results == nil {
		return false, fmt.Errorf("GET %s: the answer has no results list", endpoint)
	}
	return true, nil
}
