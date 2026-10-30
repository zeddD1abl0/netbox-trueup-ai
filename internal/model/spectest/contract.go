// Package spectest holds the tests every spec package runs
// (docs/adr/0023-check-specs-against-the-schema-with-plain-json.md): the
// specs checked for consistency with model.Check, against a committed
// snapshot of NetBox's OpenAPI schema, against the live schema of the
// disposable NetBox in integration tests, and their citations against
// NetBox's source.
package spectest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model/openapi"
)

// Contract reports every way in which specs disagree with NetBox's OpenAPI
// schema: a field the API doesn't have, or has with another kind, length,
// pattern, bounds, nullability or choices, writable where the API doesn't
// write it or the other way round, required where the API doesn't require
// it, with another default, and a field the API has that a spec neither
// describes nor ignores. specs must be consistent (model.Check) first.
// Contract returns nil when it finds nothing.
func Contract(d *openapi.Document, specs []*model.Spec) error {
	c := contract{doc: d, specs: map[string]*model.Spec{}, components: map[string]string{}}
	endpoints := map[string]*openapi.Endpoint{}
	for _, s := range specs {
		c.specs[s.Type] = s
		if s.Endpoint == "" {
			continue
		}
		e, err := d.Endpoint(s.Endpoint)
		if err != nil {
			c.fail(s.Type, "%v", err)
			continue
		}
		endpoints[s.Type] = e
		c.components[s.Type] = e.ResponseName
	}
	for _, s := range specs {
		if e := endpoints[s.Type]; e != nil {
			c.checkObject(s, e.Response, e.Request, e.Patch)
		}
	}
	return errors.Join(c.problems...)
}

type contract struct {
	doc        *openapi.Document
	specs      map[string]*model.Spec
	components map[string]string // object type -> its response component
	problems   []error
}

func (c *contract) fail(where, format string, args ...any) {
	c.problems = append(c.problems, fmt.Errorf("%s: %s", where, fmt.Sprintf(format, args...)))
}

// checkObject checks a spec against the components that describe its
// objects in a response, in a create request and in a patch request.
func (c *contract) checkObject(s *model.Spec, resp, req, patch *openapi.Schema) {
	covered := map[string]bool{}
	for i := range s.Fields {
		f := &s.Fields[i]
		where := s.Type + "." + f.Name
		if f.Ref != nil && f.Ref.Shape == model.ShapeTypeAndID {
			c.checkGeneric(where, f, resp, req, patch, covered)
			continue
		}
		covered[f.Name] = true
		c.checkField(where, f, resp, req, patch)
	}
	for _, col := range s.Children {
		if col.Nested {
			covered[col.Name] = true
			c.checkNested(s, col, resp, req, patch)
		}
	}
	for _, ig := range s.Ignored {
		covered[ig.Name] = true
		switch {
		case resp.Properties[ig.Name] == nil && req.Properties[ig.Name] == nil:
			c.fail(s.Type, "it ignores %s, which NetBox's API hasn't got", ig.Name)
		case slices.Contains(req.Required, ig.Name):
			c.fail(s.Type, "it ignores %s, which NetBox's API requires when creating one", ig.Name)
		}
	}
	all := maps.Clone(resp.Properties)
	maps.Copy(all, req.Properties)
	maps.Copy(all, patch.Properties)
	for _, name := range slices.Sorted(maps.Keys(all)) {
		if !covered[name] {
			c.fail(s.Type, "NetBox's API has %s, which the spec neither describes nor ignores", name)
		}
	}
}

// checkNested checks a nested collection's children against the items of
// the parent's field that carries them.
func (c *contract) checkNested(s *model.Spec, col model.Collection, resp, req, patch *openapi.Schema) {
	where := s.Type + "." + col.Name
	child := c.specs[col.Type]
	if child == nil {
		c.fail(where, "the collection holds %s, which has no spec", col.Type)
		return
	}
	var items [3]*openapi.Schema
	for i, parent := range []*openapi.Schema{resp, req, patch} {
		p, ok := parent.Properties[col.Name]
		if !ok {
			c.fail(where, "NetBox's API doesn't carry the collection in %s", col.Name)
			return
		}
		prop, err := c.doc.Unwrap(p)
		switch {
		case err != nil:
			c.fail(where, "%v", err)
			return
		case prop.Type != "array" || prop.Items == nil:
			c.fail(where, "NetBox's API carries the collection as a %s, not a list", prop.Type)
			return
		case prop.Nullable || prop.ReadOnly && i > 0:
			// A nested collection is left alone when its field is left out,
			// which it can't be if the field is null or read-only.
			c.fail(where, "NetBox's API carries the collection in a field that is nullable or read-only")
		}
		if items[i], _, err = c.doc.Resolve(prop.Items); err != nil {
			c.fail(where, "%v", err)
			return
		}
	}
	if slices.Contains(req.Required, col.Name) {
		c.fail(where, "NetBox's API requires the collection when creating one, though the spec says it is left alone when left out")
	}
	c.checkObject(child, items[0], items[1], items[2])
}

// checkGeneric checks a generic reference, which NetBox's API carries in two
// fields.
func (c *contract) checkGeneric(where string, f *model.Field, resp, req, patch *openapi.Schema, covered map[string]bool) {
	covered[f.Name], covered[f.Ref.TypeField], covered[f.Ref.IDField] = true, true, true
	if p, ok := resp.Properties[f.Name]; ok {
		switch prop, err := c.doc.Unwrap(p); {
		case err != nil:
			c.fail(where, "%v", err)
		case !prop.ReadOnly:
			c.fail(where, "NetBox's API gives it as a field it writes too, not as %s and %s", f.Ref.TypeField, f.Ref.IDField)
		}
	}
	if _, ok := req.Properties[f.Name]; ok {
		c.fail(where, "NetBox's API takes it as itself, not as %s and %s", f.Ref.TypeField, f.Ref.IDField)
	}
	for _, part := range []struct{ name, typ string }{{f.Ref.TypeField, "string"}, {f.Ref.IDField, "integer"}} {
		as := *f
		as.Name, as.Kind, as.Ref, as.Default = part.name, model.KindString, nil, nil
		if part.typ == "integer" {
			as.Kind = model.KindInteger
		}
		r, w, ok := c.checkAccess(where+" ("+part.name+")", &as, resp, req, patch)
		switch {
		case !ok:
		case r.Type != part.typ:
			c.fail(where, "NetBox's API gives %s as %s, not %s", part.name, r.Type, part.typ)
		case w.Schema != nil && w.Type != part.typ:
			c.fail(where, "NetBox's API takes %s as %s, not %s", part.name, w.Type, part.typ)
		}
	}
}

func (c *contract) unwrap(component *openapi.Schema, name string) (openapi.Prop, error) {
	p, ok := component.Properties[name]
	if !ok {
		return openapi.Prop{}, fmt.Errorf("NetBox's API hasn't got %s", name)
	}
	return c.doc.Unwrap(p)
}

// checkAccess checks that NetBox's API gives, writes and requires a field as
// its spec says, and returns its response and request properties, the
// latter with a nil Schema when the API doesn't write it.
func (c *contract) checkAccess(where string, f *model.Field, resp, req, patch *openapi.Schema) (r, w openapi.Prop, ok bool) {
	if resp.Properties[f.Name] == nil {
		c.fail(where, "NetBox's API doesn't give it")
		return r, w, false
	}
	r, err := c.unwrap(resp, f.Name)
	if err != nil {
		c.fail(where, "%v", err)
		return r, w, false
	}
	_, inReq := req.Properties[f.Name]
	_, inPatch := patch.Properties[f.Name]
	switch {
	case f.ReadOnly && (inReq || !r.ReadOnly):
		c.fail(where, "it is read-only, but NetBox's API writes it")
	case !f.ReadOnly && (!inReq || !inPatch):
		c.fail(where, "it is writable, but NetBox's API doesn't take it when creating and patching")
	}
	if inReq {
		if w, err = c.unwrap(req, f.Name); err != nil {
			c.fail(where, "%v", err)
			return r, w, false
		}
	}
	if required := slices.Contains(req.Required, f.Name); required != f.Required {
		c.fail(where, "its spec says required is %t, NetBox's API %t", f.Required, required)
	}
	if r.Nullable != f.Nullable || w.Schema != nil && w.Nullable != f.Nullable {
		c.fail(where, "its spec says nullable is %t, NetBox's API %t when it gives it and %t when it takes it", f.Nullable, r.Nullable, w.Nullable)
	}
	return r, w, true
}

func (c *contract) checkField(where string, f *model.Field, resp, req, patch *openapi.Schema) {
	r, w, ok := c.checkAccess(where, f, resp, req, patch)
	if !ok {
		return
	}
	c.checkKind(where, f, r, w)
	c.checkDefault(where, f, resp.Properties[f.Name], req.Properties[f.Name])
}

func (c *contract) checkKind(where string, f *model.Field, r, w openapi.Prop) {
	mismatch := func(what string) {
		c.fail(where, "its spec says it is %s, but NetBox's API gives %s", f.Kind, what)
	}
	// takes checks that NetBox's API takes a plain value as it gives it.
	takes := func() {
		if w.Schema != nil && (w.Type != r.Type || w.Format != r.Format) {
			c.fail(where, "NetBox's API takes it as %s %s but gives it as %s %s", w.Type, w.Format, r.Type, r.Format)
		}
	}
	switch f.Kind {
	case model.KindString:
		c.checkString(where, f, r, w)
	case model.KindInteger:
		if r.Type != "integer" {
			mismatch(r.Type)
			return
		}
		takes()
		c.checkBound(where, "minimum", f, f.Min, false, r.Minimum, r.ExclusiveMinimum)
		c.checkBound(where, "maximum", f, f.Max, false, r.Maximum, r.ExclusiveMaximum)
	case model.KindDecimal:
		if r.Type != "number" {
			mismatch(r.Type)
			return
		}
		takes()
		c.checkDecimalBounds(where, f, r)
	case model.KindBoolean:
		if r.Type != "boolean" {
			mismatch(r.Type)
			return
		}
		takes()
	case model.KindDate:
		if r.Type != "string" || r.Format != "date" {
			mismatch(r.Type + " " + r.Format)
			return
		}
		takes()
	case model.KindChoice:
		c.checkChoice(where, f, r, w)
	case model.KindReference:
		c.checkRef(where, f, r, w)
	case model.KindSet:
		c.checkSet(where, f, r, w)
	case model.KindChildren, model.KindNull:
		// model.Check refuses these for a field.
	}
}

// checkSet checks a set's elements against the items of the arrays NetBox's
// API gives and takes.
func (c *contract) checkSet(where string, f *model.Field, r, w openapi.Prop) {
	if r.Type != "array" || r.Items == nil || w.Schema != nil && (w.Type != "array" || w.Items == nil) {
		c.fail(where, "its spec says it is a set, but NetBox's API gives %s and takes %s", r.Type, w.Type)
		return
	}
	if f.Elem == nil {
		return // model.Check reports it
	}
	ri, err := c.doc.Unwrap(r.Items)
	if err != nil {
		c.fail(where, "%v", err)
		return
	}
	var wi openapi.Prop
	if w.Schema != nil {
		if wi, err = c.doc.Unwrap(w.Items); err != nil {
			c.fail(where, "%v", err)
			return
		}
	}
	if ri.Nullable != f.Elem.Nullable {
		c.fail(where, "its spec says its elements' nullable is %t, NetBox's API %t", f.Elem.Nullable, ri.Nullable)
	}
	c.checkKind(where+"[]", f.Elem, ri, wi)
}

// stringShape reads a string property: its most characters, its pattern,
// and whether it takes the blank string. NetBox's ColorField, which takes a
// colour or blank, is a oneOf of the colour and a string of no characters.
func stringShape(p *openapi.Schema) (maxLength int, pattern string, blank, ok bool) {
	if len(p.OneOf) == 2 {
		for i, alt := range p.OneOf {
			other := p.OneOf[1-i]
			if alt.Type == "string" && alt.MaxLength != nil && *alt.MaxLength == 0 && other.Type == "string" {
				maxLength, pattern, _, ok = stringShape(other)
				return maxLength, pattern, true, ok
			}
		}
		return 0, "", false, false
	}
	if p.Type != "string" || p.Format != "" {
		return 0, "", false, false
	}
	if p.MaxLength != nil {
		maxLength = *p.MaxLength
	}
	blank = p.MinLength == nil || *p.MinLength == 0
	return maxLength, p.Pattern, blank, true
}

func (c *contract) checkString(where string, f *model.Field, r, w openapi.Prop) {
	maxLength, pattern, _, ok := stringShape(r.Schema)
	if !ok {
		c.fail(where, "its spec says it is a string, but NetBox's API gives %s %s", r.Type, r.Format)
		return
	}
	if maxLength != f.MaxLength {
		c.fail(where, "its spec says it has at most %d characters, NetBox's API %d", f.MaxLength, maxLength)
	}
	if want := anchored(f.Pattern); pattern != want {
		c.fail(where, "its spec's pattern is %q, NetBox's API's %q", want, pattern)
	}
	if w.Schema == nil {
		return
	}
	wMaxLength, wPattern, blank, ok := stringShape(w.Schema)
	switch {
	case !ok:
		c.fail(where, "NetBox's API gives it as a string but takes it as %s %s", w.Type, w.Format)
	case wMaxLength != maxLength || wPattern != pattern:
		c.fail(where, "NetBox's API takes it with at most %d characters and the pattern %q, but gives it with %d and %q", wMaxLength, wPattern, maxLength, pattern)
	case blank != f.Blank:
		c.fail(where, "its spec says blank is %t, NetBox's API %t", f.Blank, blank)
	}
}

func anchored(pattern string) string {
	if pattern == "" {
		return ""
	}
	return "^" + pattern + "$"
}

// checkDecimalBounds checks a decimal's digits, places and bounds. The
// schema gives a DecimalField's max_digits and decimal_places only as an
// exclusive bound of 10 to the power of their difference, either way, unless
// the serializer bounds it more tightly. It never gives the places alone,
// which only NetBox's source does.
func (c *contract) checkDecimalBounds(where string, f *model.Field, r openapi.Prop) {
	whole := int64(1) // model.Check keeps Digits at most 18
	for range f.Digits - f.Places {
		whole *= 10
	}
	if f.Max == nil {
		c.checkBound(where, "maximum", f, model.NewDecimal(whole, 0), true, r.Maximum, r.ExclusiveMaximum)
	} else {
		c.checkBound(where, "maximum", f, f.Max, false, r.Maximum, r.ExclusiveMaximum)
	}
	if f.Min == nil {
		c.checkBound(where, "minimum", f, model.NewDecimal(-whole, 0), true, r.Minimum, r.ExclusiveMinimum)
	} else {
		c.checkBound(where, "minimum", f, f.Min, false, r.Minimum, r.ExclusiveMinimum)
	}
}

// checkBound checks one of a field's bounds, want, which its digits imply
// when implied is set, against the schema's, got. The spec's bound must be
// the schema's, or tighter where NetBox's model enforces more than its API
// states, as it refuses a negative weight when it saves one: the field must
// then cite where, in its Source, and integration tests check it against
// NetBox. A bound the spec states is inclusive.
func (c *contract) checkBound(where, which string, f *model.Field, want model.Value, implied bool, got *json.Number, exclusive bool) {
	cited := f.Source != (model.Source{})
	switch {
	case want == nil && got == nil:
		return
	case got == nil && !implied && cited:
		return // tighter than no bound at all
	case want == nil || got == nil:
		c.fail(where, "its spec's %s is %s, NetBox's API's %s", which, describe(want), numberString(got))
		return
	}
	n, err := model.ParseDecimal(got.String())
	if err != nil {
		c.fail(where, "NetBox's API's %s %s: %v", which, got, err)
		return
	}
	var w model.Decimal
	switch want := want.(type) {
	case model.Decimal:
		w = want
	case model.Integer:
		w = model.NewDecimal(int64(want), 0)
	}
	// tighter is positive when the spec's bound lets fewer values through.
	tighter := w.Cmp(n)
	if which == "maximum" {
		tighter = -tighter
	}
	switch {
	case tighter == 0 && exclusive == implied:
		// The same bound.
	case !implied && tighter > 0:
		if !cited {
			c.fail(where, "its spec's %s %s is tighter than NetBox's API's %s, so it needs a Source saying where NetBox enforces it", which, w, got)
		}
	default:
		// Looser than the API's, an inclusive bound the API states as
		// exclusive included, or not the bound the digits imply.
		c.fail(where, "its spec's %s is %s (exclusive %t), NetBox's API's %s (exclusive %t)", which, w, implied, got, exclusive)
	}
}

func (c *contract) checkChoice(where string, f *model.Field, r, w openapi.Prop) {
	value, ok := r.Properties["value"]
	if r.Type != "object" || !ok {
		c.fail(where, "its spec says it is a choice, but NetBox's API gives %s, not {value, label}", r.Type)
		return
	}
	got, _, _, err := openapi.EnumValues(value)
	if err != nil {
		c.fail(where, "%v", err)
		return
	}
	if f.Choices != nil && !slices.Equal(got, f.Choices.Values) {
		c.fail(where, "its choices are %v, NetBox's API's %v: run make generate?", f.Choices.Values, got)
	}
	if w.Schema == nil {
		return
	}
	writes, blank, _, err := openapi.EnumValues(w.Schema)
	switch {
	case err != nil:
		c.fail(where, "%v", err)
	case !slices.Equal(writes, got):
		c.fail(where, "NetBox's API takes the choices %v but gives %v", writes, got)
	case blank != f.Blank:
		c.fail(where, "its spec says blank is %t, NetBox's API %t", f.Blank, blank)
	}
}

func (c *contract) checkRef(where string, f *model.Field, r, w openapi.Prop) {
	if f.Ref == nil {
		return
	}
	switch f.Ref.Shape {
	case model.ShapeID:
		if r.Type != "integer" || w.Schema != nil && w.Type != "integer" {
			c.fail(where, "its spec says NetBox's API carries it as an ID, but it gives %s and takes %s", r.Type, w.Type)
		}
	case model.ShapeNested:
		if len(f.Ref.Types) != 1 {
			c.fail(where, "a nested reference refers to one type")
			return
		}
		target := c.components[f.Ref.Types[0]]
		if r.Component != "Brief"+target && r.Component != "Nested"+target {
			gives := r.Component
			if gives == "" {
				gives = "a " + r.Type
			}
			c.fail(where, "its spec says it refers to a %s, which NetBox's API gives as %s, but it gives %s",
				f.Ref.Types[0], "Brief"+target, gives)
		}
		if w.Schema != nil && !c.takesIDOrBrief(where, w.Schema, target) {
			c.fail(where, "NetBox's API doesn't take it as an ID or a brief %s", target)
		}
	case model.ShapeTypeAndID:
		// checkGeneric checks those.
	}
}

// takesIDOrBrief reports whether a request property takes an ID or a brief
// object of the target component, as NetBox's nested serializers do.
func (c *contract) takesIDOrBrief(where string, p *openapi.Schema, target string) bool {
	var id, brief bool
	for _, alt := range p.OneOf {
		prop, err := c.doc.Unwrap(alt)
		switch {
		case err != nil:
			c.fail(where, "%v", err)
			return false
		case prop.Type == "integer":
			id = true
		case prop.Component == "Brief"+target+"Request" || prop.Component == "Nested"+target+"Request":
			brief = true
		}
	}
	return id && brief
}

// checkDefault checks a default the schema gives, which it does only for a
// default the serializer declares. A model's default doesn't appear in the
// schema, so only NetBox's source shows most defaults.
func (c *contract) checkDefault(where string, f *model.Field, schemas ...*openapi.Schema) {
	for _, s := range schemas {
		if s == nil || len(s.Default) == 0 {
			continue
		}
		got, err := parseDefault(f.Kind, s.Default)
		if err != nil {
			c.fail(where, "NetBox's API's default %s: %v", s.Default, err)
			continue
		}
		if !model.Equal(got, f.Default) {
			c.fail(where, "its spec's default is %s, NetBox's API's %s", describe(f.Default), got)
		}
	}
}

func describe(v model.Value) string {
	if v == nil {
		return "none"
	}
	return v.String()
}

func numberString(n *json.Number) string {
	if n == nil {
		return "none"
	}
	return n.String()
}

// parseDefault reads a default from the schema as a value of the kind.
func parseDefault(kind model.Kind, raw json.RawMessage) (model.Value, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return model.Null{}, nil
	}
	var err error
	switch kind {
	case model.KindDecimal:
		var n json.Number
		if err = json.Unmarshal(raw, &n); err == nil {
			return model.ParseDecimal(n.String())
		}
	case model.KindInteger:
		var n int64
		if err = json.Unmarshal(raw, &n); err == nil {
			return model.Integer(n), nil
		}
	case model.KindBoolean:
		var b bool
		if err = json.Unmarshal(raw, &b); err == nil {
			return model.Boolean(b), nil
		}
	case model.KindString, model.KindChoice:
		var str string
		if err = json.Unmarshal(raw, &str); err == nil {
			if kind == model.KindChoice {
				return model.Choice(str), nil
			}
			return model.String(str), nil
		}
	default:
		return nil, fmt.Errorf("the spec can't check a default for a %s", kind)
	}
	return nil, fmt.Errorf("it isn't a %s: %w", kind, err)
}
