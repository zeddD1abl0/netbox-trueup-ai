// Package openapi reads NetBox's OpenAPI schema, as NetBox publishes it at
// /api/schema/, with encoding/json alone
// (docs/adr/0023-check-specs-against-the-schema-with-plain-json.md). It
// finds the components that describe an endpoint's objects, trims a schema
// down to the endpoints specs use for the snapshot that tests check specs
// against, and lists the values of choice fields, from which choices are
// generated. It does no I/O.
//
// NetBox builds its schema with drf-spectacular, with COMPONENT_SPLIT_REQUEST
// on, so each object type has a response component, such as DeviceType, and
// request components for creating and patching, such as
// WritableDeviceTypeRequest and PatchedWritableDeviceTypeRequest
// (SPECTACULAR_SETTINGS in netbox/netbox/settings.py, and NetBoxAutoSchema in
// netbox/core/api/schema.py).
package openapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Schema is an OpenAPI 3.0 schema object, with the keywords NetBox's schema
// uses that specs are checked against.
type Schema struct {
	Ref              string             `json:"$ref"`
	Type             string             `json:"type"`
	Format           string             `json:"format"`
	Nullable         bool               `json:"nullable"`
	ReadOnly         bool               `json:"readOnly"`
	Enum             []json.RawMessage  `json:"enum"`
	MaxLength        *int               `json:"maxLength"`
	MinLength        *int               `json:"minLength"`
	Minimum          *json.Number       `json:"minimum"`
	Maximum          *json.Number       `json:"maximum"`
	ExclusiveMinimum bool               `json:"exclusiveMinimum"`
	ExclusiveMaximum bool               `json:"exclusiveMaximum"`
	Pattern          string             `json:"pattern"`
	Default          json.RawMessage    `json:"default"`
	Properties       map[string]*Schema `json:"properties"`
	Required         []string           `json:"required"`
	Items            *Schema            `json:"items"`
	OneOf            []*Schema          `json:"oneOf"`
	AllOf            []*Schema          `json:"allOf"`
}

type media struct {
	Schema *Schema `json:"schema"`
}

type operation struct {
	RequestBody *struct {
		Content map[string]media `json:"content"`
	} `json:"requestBody"`
	Responses map[string]struct {
		Content map[string]media `json:"content"`
	} `json:"responses"`
}

// Document is an OpenAPI document, such as NetBox's schema or a snapshot of
// part of it.
type Document struct {
	OpenAPI string `json:"openapi"`
	Info    struct {
		// Version is NetBox's, such as "4.7.2-Docker-5.1.1 (4.7)".
		Version string `json:"version"`
	} `json:"info"`
	Paths      map[string]map[string]*operation `json:"paths"`
	Components struct {
		Schemas map[string]*Schema `json:"schemas"`
	} `json:"components"`
}

// Load reads an OpenAPI 3.0 document in JSON.
func Load(data []byte) (*Document, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	var d Document
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("read the OpenAPI schema: %w", err)
	}
	if !strings.HasPrefix(d.OpenAPI, "3.0.") {
		return nil, fmt.Errorf("read the OpenAPI schema: it is OpenAPI %q, not 3.0", d.OpenAPI)
	}
	return &d, nil
}

// refPrefix starts every $ref in NetBox's schema: it refers only to its own
// components.
const refPrefix = "#/components/schemas/"

// maxRefs is the longest chain of $refs Resolve follows. NetBox's schema
// never chains them.
const maxRefs = 8

// Resolve returns the schema s refers to, following $refs, with its
// component's name, or s itself and no name if it isn't a $ref.
func (d *Document) Resolve(s *Schema) (*Schema, string, error) {
	name := ""
	for hops := 0; ; hops++ {
		switch {
		case s == nil:
			return nil, "", errors.New("a schema is missing")
		case s.Ref == "":
			return s, name, nil
		case hops == maxRefs:
			return nil, "", fmt.Errorf("a $ref chain is longer than %d", maxRefs)
		}
		ref := s.Ref
		var ok bool
		if name, ok = strings.CutPrefix(ref, refPrefix); !ok {
			return nil, "", fmt.Errorf("$ref %q isn't to a component of the schema", ref)
		}
		if s = d.Components.Schemas[name]; s == nil {
			return nil, "", fmt.Errorf("$ref %q names no component", ref)
		}
	}
}

// Prop is a property of a component, unwrapped from the allOf with a single
// schema that drf-spectacular wraps a nullable $ref in: the schema it
// refers to, the referred component's name if there is one, and whether the
// property is nullable or read-only, which the wrapper or the schema can say.
type Prop struct {
	*Schema
	Component string
	Nullable  bool
	ReadOnly  bool
}

// Unwrap returns the property s describes.
func (d *Document) Unwrap(s *Schema) (Prop, error) {
	if s == nil {
		return Prop{}, errors.New("a schema is missing")
	}
	p := Prop{Nullable: s.Nullable, ReadOnly: s.ReadOnly}
	inner := s
	if len(s.AllOf) == 1 && s.Ref == "" && s.Type == "" {
		inner = s.AllOf[0]
	}
	resolved, name, err := d.Resolve(inner)
	if err != nil {
		return Prop{}, err
	}
	p.Schema, p.Component = resolved, name
	p.Nullable = p.Nullable || resolved.Nullable
	p.ReadOnly = p.ReadOnly || resolved.ReadOnly
	return p, nil
}

// Endpoint is what NetBox's schema says about one list endpoint: the
// components of the objects it lists, takes when one is created, and takes
// when one is patched.
type Endpoint struct {
	Path                                 string
	Response, Request, Patch             *Schema
	ResponseName, RequestName, PatchName string
}

// Endpoint finds the components of the list endpoint at path, such as
// /api/dcim/device-types/: the objects in a page that a GET lists, what a
// POST takes to create one (a POST also takes a list, which NetBox creates
// together), and what a PATCH of one object, at path{id}/, takes.
func (d *Document) Endpoint(path string) (*Endpoint, error) {
	e := &Endpoint{Path: path}
	list, err := d.body(path, "get", "200")
	if err != nil {
		return nil, err
	}
	page, _, err := d.Resolve(list)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	results, ok := page.Properties["results"]
	if !ok || results.Items == nil {
		return nil, fmt.Errorf("GET %s doesn't list objects in results", path)
	}
	if e.Response, e.ResponseName, err = d.Resolve(results.Items); err != nil {
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}

	create, err := d.body(path, "post", "")
	if err != nil {
		return nil, err
	}
	if len(create.OneOf) != 2 {
		return nil, fmt.Errorf("POST %s doesn't take one object or a list of them", path)
	}
	if e.Request, e.RequestName, err = d.Resolve(create.OneOf[0]); err != nil {
		return nil, fmt.Errorf("POST %s: %w", path, err)
	}

	patch, err := d.body(path+"{id}/", "patch", "")
	if err != nil {
		return nil, err
	}
	if e.Patch, e.PatchName, err = d.Resolve(patch); err != nil {
		return nil, fmt.Errorf("PATCH %s{id}/: %w", path, err)
	}
	return e, nil
}

// body returns the JSON schema of an operation's request body, or with a
// status code, of that response's body.
func (d *Document) body(path, method, status string) (*Schema, error) {
	op := d.Paths[path][method]
	if op == nil {
		return nil, fmt.Errorf("the schema has no %s %s", strings.ToUpper(method), path)
	}
	var content map[string]media
	if status == "" {
		if op.RequestBody == nil {
			return nil, fmt.Errorf("%s %s takes no body", strings.ToUpper(method), path)
		}
		content = op.RequestBody.Content
	} else {
		content = op.Responses[status].Content
	}
	m, ok := content["application/json"]
	if !ok || m.Schema == nil {
		return nil, fmt.Errorf("%s %s has no JSON body", strings.ToUpper(method), path)
	}
	return m.Schema, nil
}

// EnumValues returns s's enum as strings, without the blank string and null,
// which NetBox lists among a nullable choice field's values, and whether
// they were there.
func EnumValues(s *Schema) (values []string, blank, null bool, err error) {
	for _, raw := range s.Enum {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			null = true
			continue
		}
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, false, false, fmt.Errorf("enum value %s isn't a string", raw)
		}
		if v == "" {
			blank = true
			continue
		}
		values = append(values, v)
	}
	return values, blank, null, nil
}
