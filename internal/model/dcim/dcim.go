// Package dcim holds the specs of NetBox's dcim object types that TrueUp
// handles (docs/adr/0008-describe-object-types-with-specs.md): manufacturers
// and device types so far, with component templates to follow in roadmap
// steps 11 and 12. Each spec follows NetBox 4.7.2's source, which it cites,
// and agrees with its OpenAPI schema, a snapshot of which is in
// testdata/openapi.json. The choice values in choices_gen.go are generated
// from that snapshot by `make generate`.
//
// Specs are shared: treat them, and everything they hold, as read-only.
//
// Two rules hold for every string field here, so the specs don't repeat
// their sources: NetBox's REST API and its own import both trim white space
// from both ends of a string (Django REST Framework's CharField, with
// trim_whitespace on, and Django's forms.CharField, with strip on), and
// strings compare exactly, case included, in NetBox's uniqueness rules,
// since PostgreSQL's collations, natural_sort included, are deterministic.
package dcim

import "github.com/zeddD1abl0/netbox-trueup-ai/internal/model"

// Specs returns the specs of the dcim object types TrueUp handles, in an
// order that creates each after those it depends on.
func Specs() []*model.Spec {
	return []*model.Spec{manufacturer, deviceType}
}

// choices returns the values of a choice field from the generated
// choiceValues: component is the schema's response component for the
// object type, such as DeviceType. A field missing there gets no values,
// which model.Check reports.
func choices(component, field string) []string {
	return choiceValues[component+"."+field]
}

// The ignored fields every NetBox object type in these specs has.
var (
	ignoredBookkeeping = []model.Ignored{
		{Name: "id", Reason: "NetBox's ID for the object, which references and links use, not a field TrueUp manages"},
		{Name: "url", Reason: "read-only: the object's API URL"},
		{Name: "display_url", Reason: "read-only: the object's web UI URL"},
		{Name: "display", Reason: "read-only: the object's name as NetBox shows it"},
		{Name: "created", Reason: "read-only: when NetBox created the object"},
		{Name: "last_updated", Reason: "read-only: when NetBox last changed the object"},
	}
	ignoredNetBoxModel = []model.Ignored{
		{Name: "owner", Reason: "a reference to a users.owner, which has no spec yet; TrueUp leaves it as it is"},
		{Name: "tags", Reason: "references to extras.tag, which has no spec yet; TrueUp leaves them as they are"},
		{Name: "custom_fields", Reason: "custom fields need a design of their own, after milestone 1; TrueUp leaves them as they are"},
	}
)

// slugPattern is a SlugField's pattern: Django's validate_slug, which the
// schema gives as ^[-a-zA-Z0-9_]+$.
const slugPattern = `[-a-zA-Z0-9_]+`
