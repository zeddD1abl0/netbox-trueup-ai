package openapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Trim returns the part of an OpenAPI schema that describes the given list
// endpoints, for the snapshot tests check specs against. For each endpoint
// it keeps what Document.Endpoint reads: the list path's GET and POST and the
// detail path's ({id}/) PATCH, and of those only the request bodies and the
// success responses. It keeps every component they refer to, directly or
// through other components, and the schema's openapi and info. The result is
// indented JSON with its keys in order and numbers as written, so that a
// snapshot changes only where NetBox's schema does.
func Trim(schema []byte, endpoints []string) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(schema))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("read the OpenAPI schema: %w", err)
	}
	paths, _ := doc["paths"].(map[string]any)
	components, _ := doc["components"].(map[string]any)
	schemas, _ := components["schemas"].(map[string]any)

	keptPaths := map[string]any{}
	for _, e := range endpoints {
		if _, ok := paths[e]; !ok {
			return nil, fmt.Errorf("the schema has no endpoint %s", e)
		}
		for path, methods := range map[string][]string{e: {"get", "post"}, e + "{id}/": {"patch"}} {
			ops, ok := paths[path].(map[string]any)
			if !ok {
				continue
			}
			kept := map[string]any{}
			for _, method := range methods {
				if op, ok := ops[method].(map[string]any); ok {
					kept[method] = trimOperation(op)
				}
			}
			keptPaths[path] = kept
		}
	}

	keptSchemas := map[string]any{}
	pending := refs(keptPaths, nil)
	for len(pending) > 0 {
		name := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if _, done := keptSchemas[name]; done {
			continue
		}
		s, ok := schemas[name]
		if !ok {
			return nil, fmt.Errorf("the schema refers to %s, which isn't one of its components", name)
		}
		keptSchemas[name] = s
		pending = refs(s, pending)
	}

	out := map[string]any{
		"openapi":    doc["openapi"],
		"info":       doc["info"],
		"paths":      keptPaths,
		"components": map[string]any{"schemas": keptSchemas},
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return nil, fmt.Errorf("write the trimmed schema: %w", err)
	}
	return buf.Bytes(), nil
}

// trimOperation keeps an operation's request body and success responses.
func trimOperation(op map[string]any) map[string]any {
	kept := map[string]any{}
	if body, ok := op["requestBody"]; ok {
		kept["requestBody"] = body
	}
	if responses, ok := op["responses"].(map[string]any); ok {
		success := map[string]any{}
		for code, r := range responses {
			if strings.HasPrefix(code, "2") {
				success[code] = r
			}
		}
		kept["responses"] = success
	}
	return kept
}

// refs appends the names of the components v refers to, in a stable order.
func refs(v any, names []string) []string {
	switch v := v.(type) {
	case map[string]any:
		if ref, ok := v["$ref"].(string); ok {
			if name, ok := strings.CutPrefix(ref, refPrefix); ok {
				names = append(names, name)
			}
		}
		for _, k := range slices.Sorted(maps.Keys(v)) {
			names = refs(v[k], names)
		}
	case []any:
		for _, e := range v {
			names = refs(e, names)
		}
	}
	return names
}
