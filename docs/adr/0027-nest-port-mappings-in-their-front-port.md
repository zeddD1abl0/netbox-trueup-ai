---
status: accepted
date: 2026-11-17
decision-makers: "@zeddD1abl0"
---

# Compare port mappings as children of their front port, keyed by position

## Context and problem

Roadmap step 12 compares a device type's front and rear port templates, and the mappings between them. A mapping ties one position of a front port to one position of a rear port, such as each of the 12 fibres of an MPO rear port to an LC front port. NetBox 4.7.2 and devicetype-library hold mappings in different shapes:

- NetBox's `PortTemplateMapping` has no endpoint of its own (`PortMappingBase._netbox_private` in `dcim/models/base.py`). Its API carries each mapping twice, with no ID: in the front port template's `rear_ports`, as `position`, `rear_port` by ID and `rear_port_position`, and in the rear port template's `front_ports`, the other way round. Writing `rear_ports` replaces the front port's whole set (`PortSerializer` in `dcim/api/serializers_/base.py`), and `reconcile_port_mappings` (`dcim/utils.py`) tells a front port's mappings apart by their position on it.
- The library has a top-level `port-mappings` list, naming both ports. NetBox's import reads it with `PortTemplateMappingImportForm` (`dcim/forms/object_import.py`), after the rear and front ports, finding each port by name among the device type's.

[ADR 0026](0026-compare-children-by-their-key.md) compares a device type's children by their key, with a row for each child, and leaves children only NetBox has unmanaged. It didn't settle:

- where mappings go, and what tells them apart;
- how a mapping's rear port compares;
- what a front port NetBox lacks means for its mappings;
- what mappings only NetBox has mean, given NetBox replaces the set when it is written.

## Decision drivers

- Compare in the shape NetBox's API reads and writes, since milestone 2 writes through it.
- Keep ADR 0026's rules, and its promise that nested collections add steps to a path, not shapes.
- A difference is something importing would change, and counts once.
- Read the library as NetBox's import reads it, refusing what it refuses.

## Considered options

Where mappings go:

1. Nested in their front port, as its `rear_ports`, keyed by the front port's position
2. A collection of their own on the device type, keyed by front port and position, as the library lists them

Mappings only NetBox has, on a front port the definition has:

1. Reported, not managed, as for any child only NetBox has
2. Managed as a set, so that each is a difference

## Decision

Chosen options: "nested in their front port" and "reported, not managed".

**Specs.** A front port template has a nested collection, `rear_ports`, of `dcim.porttemplatemapping` children, keyed by `position`, which NetBox keeps unique within a front port (`PortMappingBase.Meta`). A mapping's `rear_port` is a reference within the device type, which NetBox's API carries as an ID alone, and `rear_port_position` defaults to 1. A rear port template ignores its `front_ports`, the same mappings again. NetBox's model bounds both positions from 1 to 1024, and its import checks them; its API runs none of a mapping's model checks, since `ValidatedModelSerializer.validate` (`netbox/api/serializers/base.py`) leaves the nested mappings out before it runs `full_clean`, so `position`'s bounds cite `TighterBounds`.

**Paths.** A mapping's rows go on from its front port's, such as `["front_ports", {"name": "FP1"}, "rear_ports", {"position": 1}, "rear_port"]`, which text writes as `front_ports["FP1"].rear_ports[1].rear_port`. A mapping's row has a null `netbox_id`, since NetBox gives mappings no ID, and so does its collision.

**A mapping's rear port** compares by the name of the rear port it refers to, as a power outlet's power port does. NetBox's ID is given the name of the device type's rear port with that ID; one of another device type's, which NetBox's API takes though its model forbids it, keeps only its ID, and so differs.

**A front port NetBox lacks** is one difference. Its mappings' rows show NetBox's cells unset, not false, as its fields' rows do. A front port NetBox holds twice, which its constraint forbids, is a conflict, and its mappings aren't compared either. A front port only NetBox has shows its own row alone.

**Mappings only NetBox has**, on a front port the definition has, are reported but not managed, so a device type can be in step with them. A front port with no mappings in the library leaves its `rear_ports` unset, and so manages none of NetBox's. Two mappings at one position of a NetBox front port make the device type a conflict, as two children with one key do, but only where TrueUp compares that front port's mappings.

**Reading NetBox.** A front port template's listing asks for `rear_ports` with the spec's fields (`fields=...,rear_ports`), which NetBox gives whole, with one more database query per page. Nested children are checked as their spec says, as the parent is.

**Reading the library.** Each item of `port-mappings` is folded into its front port's `rear_ports`, with the front port's position as `position`. A mapping is an error, as NetBox's import refuses it, where it names a port the file hasn't got, exactly (NetBox's import then fails with a server error and saves nothing), where its rear port position is beyond the rear port's positions (`PortMappingBase.clean`), and where it maps a front port's position, or a rear port's, that another mapping does (`PortMappingBase.Meta`). A front port position beyond the front port's positions is no error to NetBox's import, so it is none to TrueUp. The order of the lists doesn't matter: NetBox creates mappings after the ports.

### Consequences

- Good, because the comparison has the shape milestone 2 writes: a front port and its `rear_ports`, in one request.
- Good, because a missing front port is one difference, as importing would make one change.
- Good, because ADR 0026's rows, paths and rules carry over: the engine compares nested collections at any depth, and a nested child's reference may name a child of any object above it.
- Bad, because writing `rear_ports` replaces a front port's mappings, so milestone 2 must send the mappings only NetBox has back with the definition's, or leave `rear_ports` out when they already agree, to leave them as they are.
- Bad, because TrueUp, like NetBox's import, doesn't check that a NetBox mapping's position is in range: NetBox can hold a mapping its import would refuse, and it compares as it is.

## Pros and cons of the options

### A collection of mappings on the device type

- Good, because it reads as the library lists them, and the key, front port and position, says all a mapping is.
- Bad, because NetBox has no endpoint for mappings and writes them only through their front port, so every read and write would reshape them.
- Bad, because a missing front port would count once for itself and once for each mapping.

### Mappings only NetBox has, managed as a set

- Good, because NetBox writes them as a set.
- Bad, because importing the device type in NetBox's own way would leave them, so they aren't something importing changes, and the rule would differ from every other child's.

## More information

- NetBox 4.7.2: `PortMappingBase` in `dcim/models/base.py`; `PortTemplateMapping`, `FrontPortTemplate` and `RearPortTemplate` in `dcim/models/device_component_templates.py`; `FrontPortTemplateMappingSerializer` and `FrontPortTemplateSerializer` in `dcim/api/serializers_/devicetype_components.py`; `PortSerializer` in `dcim/api/serializers_/base.py`; `reconcile_port_mappings` in `dcim/utils.py`; `PortTemplateMappingImportForm` in `dcim/forms/object_import.py`; and `DeviceTypeImportView.related_object_forms` in `dcim/views.py`.
- `TestMappingPositionsAgainstNetBox` in `internal/model/dcim` checks that NetBox's API takes a position its import refuses, and `TestNestedDefaults` the mapping's defaults. The parity test, `TestCompareParity` in `cmd/netbox-trueup-ai`, raises a rear port's positions, points a mapping at another rear port, adds a front port, and deletes one, each making exactly its difference.
