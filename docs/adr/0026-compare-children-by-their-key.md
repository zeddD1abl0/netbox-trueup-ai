---
status: accepted
date: 2026-11-16
decision-makers: "@zeddD1abl0"
---

# Compare a device type's component templates by their key, with a row for each child, and leave children only NetBox has unmanaged

## Context and problem

Roadmap step 11 compares a device type's console, power, interface and bay templates with NetBox's. [ADR 0006](0006-manage-only-linked-objects.md) settled that components a definition lists are matched within their parent by NetBox's uniqueness rule, and that those only NetBox has are reported, never removed. [ADR 0025](0025-match-by-netbox-uniqueness-rules.md) settled the side-by-side result, and promised that a row's path would gain steps into child collections. Neither settled:

- how a child's presence, and its fields, look in the result, which `compare --output json` prints and the database records ([ADR 0018](0018-cli-conventions.md));
- which of a child's differences count, and what a child only NetBox has does to `in_step`;
- what NetBox's own import gives a field a component leaves out;
- what to do when NetBox holds two children with one key;
- how much of NetBox to read, and how large a comparison of the whole library becomes.

## Decision drivers

- One structure for fields and children, which step 12's nested port mappings and inventory tree, the web UI and plans can grow into, without breaking the CLI's JSON.
- A difference is something importing would change. TrueUp never removes a child.
- Values compare as NetBox stores them, and a definition reads its children as NetBox's import does.
- Read cost and output size follow what is compared, not everything NetBox holds.

## Considered options

A child in the result:

1. A row for each child, its path ending at its key, saying whether each version has it, and rows for its fields below it
2. Rows for the child's fields alone, a child missing from NetBox showing as unset cells
3. A list of children apart from the rows

Children only NetBox has:

1. Reported, not managed, so not differences
2. Differences, so the device type isn't in step

A child's field rows:

1. Where the definition sets the field, or NetBox holds something other than what creating the child would give it
2. Every field any version sets, as for the object's own fields

## Decision

Chosen options: "a row for each child", "reported, not managed" and "where the definition sets it, or NetBox holds something other than the default".

**Paths.** A row's path is a list of steps: a field's or a collection's name, as a string, and a child's key, as an object, such as `["interfaces", {"name": "eth0"}, "type"]`. In text, `interfaces["eth0"].type`. A device type's collections are `console_ports`, `console_server_ports`, `power_ports`, `power_outlets`, `interfaces`, `module_bays` and `device_bays`, each keyed by name; step 12 adds front and rear ports and inventory items.

**A row for each child.** Its path ends at the child's key, and its cells say, as `true` or `false`, whether each version has the child. It carries NetBox's child's ID as `netbox_id`, which is null on a field's row. A child the definition has is managed; a child NetBox lacks is one difference, not one for each of its fields.

**Children only NetBox has** have a row that differs but isn't managed: they don't count toward `differences`, and a device type can be `in_step` with them. Text output lists them apart, as children TrueUp leaves as they are.

**A child's fields** have rows where the definition sets the field, or where NetBox holds something other than the field's default, which creating the child would give it. The field that refers to the parent and the key's fields have none: the path holds them. A reference to another child of the same device type, such as a power outlet's power port, compares by the key of what it refers to. NetBox's reference, by ID, is given the key of the device type's child with that ID; one to another device type's, which NetBox forbids, keeps only its ID, and so differs.

**Keys count exactly**, as NetBox's constraints compare them: the definition's as NetBox would store them, normalized through the spec, NetBox's as it gives them. Two NetBox children of one device type with one key, which NetBox's constraint forbids, make the device type a conflict: each is a collision, with the child's path, and neither is compared.

**Reading NetBox.** The templates of each device type a definition matches, one listing for each collection, through `device_type_id`, asking for the spec's fields alone. A token that can't read a template type fails the command, naming it.

**Reading the library.** Each list maps to its collection, with the keys NetBox's import form takes and the library's schema allows, as for a device type's own fields. NetBox's import fills a field an item leaves out from the form's initial value (`BulkImportView._save_object`), which for these seven templates is the REST API's default, or a blank choice the API reads back as null. An interface's `bridge`, which the library's schema allows but the import form doesn't take, and a console port's `_is_power_source`, a hint for the library's own tests, are read and checked but not kept. A name given twice, once trimmed, and a power outlet naming a power port the device type hasn't got, are errors, as NetBox's import refuses both. A whole number written as a float, such as `maximum_draw: 2.0`, reads as Django's `IntegerField` reads it.

**Recording.** The recorded JSON grows in place: `differences` counts children too, and no migration is needed.

### Consequences

- Good, because one row type holds fields and children, and a child's path tells where it is, so step 12's nested collections add steps, not shapes.
- Good, because a missing interface is one difference, as importing would make one change.
- Good, because a NetBox with extra templates, such as ones added by hand, still compares as in step, as importing would leave them.
- Bad, because a path step is now a string or an object, so a script that read every step as a string must change. `json.go` and the usage guide promised it since step 10.
- Bad, because a child NetBox holds at its defaults shows only the row for the child, not its fields, in the side-by-side table.
- Bad, because a device type NetBox holds twice by one name, which needs another tool to break NetBox's rules, can't have its children compared.

## Pros and cons of the options

### Rows for the child's fields alone

- Good, because there's one kind of row.
- Bad, because a child with only a key, such as a device bay with only a name, would have no row to say it is missing, and a missing child would count once for each field it sets.

### A list of children apart from the rows

- Good, because it reads plainly.
- Bad, because it is a second shape beside the rows, for the UI and plans to follow, and a child's fields would still need rows.

### Children only NetBox has as differences

- Good, because the definition and NetBox don't agree.
- Bad, because importing would change nothing, so `--exit-code` would fail for a device type TrueUp can't bring further into step.

### Every field any version sets

- Good, because it is what the object's own fields do.
- Bad, because NetBox gives every field of every child: a NetBox holding the whole library would add about 1.5 million rows for interfaces alone, almost all of them defaults no one set.

## More information

- NetBox 4.7.2: `ComponentTemplateModel.Meta` and `ModularComponentTemplateModel.Meta` in `dcim/models/device_component_templates.py` (`(device_type, name)`), `InterfaceTemplate.Meta` (`(parent, channel_id)`), `DeviceTypeImportView.related_object_forms` in `dcim/views.py`, the import forms in `dcim/forms/object_import.py`, `BulkImportView._save_object` in `netbox/views/generic/bulk_views.py`, `DeviceTypeComponentFilterSet` in `dcim/filtersets.py`, `ChoiceField` in `netbox/api/fields.py`, and `NetBoxAutoSchema.get_writable_class` in `core/api/schema.py`, which rebuilds choice fields and nested serializers from the model for requests.
- The parity test, `TestCompareParity` in `cmd/netbox-trueup-ai`, imports the test library through NetBox's own import, and finds exactly the difference each change to a template makes: a field, a reference to another template, a template added, and one deleted.
