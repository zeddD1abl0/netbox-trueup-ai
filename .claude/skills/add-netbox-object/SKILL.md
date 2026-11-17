---
name: add-netbox-object
description: Teach TrueUp a new NetBox object type, or a new kind of child such as a component template - its spec, its schema snapshot and choices, its tests against NetBox, how each format reads it, how compare reads it from NetBox, the parity test and the docs. Use when adding or removing an object type or a child collection, not for changing one field of an existing spec (netbox-parity covers that).
---

# Adding a NetBox object type

Every object type TrueUp handles is a spec ([ADR 0008](../../../docs/adr/0008-describe-object-types-with-specs.md)): comparison, reading NetBox, `netbox check` and, from milestone 2, planning all work from specs, so a new type is mostly a spec, its tests, and how each format reads it. Roadmap steps 8 to 12 added manufacturers, device types and their component templates this way; their pull requests show each step below.

## Before you start

- **Check NetBox still wants it.** Read the release notes and the model's documentation in NetBox's source (`make reference`). TrueUp doesn't build on a model NetBox has deprecated, as [ADR 0028](../../../docs/adr/0028-no-inventory-item-templates.md) records for inventory item templates.
- **Check what the library holds.** Survey the whole of `.cache/devicetype-library` with a short script: how many files use the list, which keys, the largest and oddest values, references that don't resolve, repeats. Keep the script and its log in `.cache/` for the pull request.
- **Put the design decisions to the maintainer** before writing code, each with a recommendation: the spec's fields and gaps, keys and references, how the format maps the list, what is an error, how NetBox is read, and what the parity test changes. Record the answers in a memory note.

## Steps

1. **Read NetBox's source first**, with the `netbox-parity` skill: the model and its base classes, `Meta.constraints`, `clean`, the serializer (and any nested serializer), the import form, the filterset that lists children by parent, and the choice sets. Probe what the source leaves unclear in the disposable NetBox's shell, in a transaction that rolls back. Keep the probe in `.cache/`.

2. **Write the spec** in `internal/model/<app>/`, as the skill says, and add it to the package's `Specs()`, after every type it depends on.
   - A child type, such as a component template, has no `Identity`: its parent's collection tells it apart. Add a `Collection` to the parent's spec, with the child's `Parent` field, its `Key`, and a `Gap` where NetBox doesn't keep the key unique.
   - Children with no endpoint, which the parent's serializer carries, such as a front port's mappings, are a `Nested` collection on the parent, with a `Source` saying where NetBox keeps the key unique, and their spec has no `Endpoint`.
   - A reference to another child of the same object, however deep, is `Within` that object's type, and compares by the child's key ([ADR 0026](../../../docs/adr/0026-compare-children-by-their-key.md), [ADR 0027](../../../docs/adr/0027-nest-port-mappings-in-their-front-port.md)).
   - Where NetBox's import gives a field another default than its REST API, set `ImportDefault` or `ImportRequired`. The import fills what a record leaves out from the form's initial values (`BulkImportView._save_object`).

3. **Refresh the snapshot and choices:** `make schema` (Docker, about 5 minutes). Review the diff of `testdata/openapi.json`: it should gain exactly the new endpoint's components. Run it from a copy of the tree if you keep editing meanwhile, and copy back the snapshot and `choices_gen.go`.

4. **Test the spec.** `make test` runs `spectest.Run`. In the package's integration test:
   - give `TestDefaults` a factory for the type, or check a nested type's defaults through its parent, as `TestNestedDefaults` does;
   - add the type's list to `TestImportDefaults`'s file, with no more than NetBox needs, and check what the import fills;
   - add the new bounds, lengths and choices to `TestValidationAgainstNetBox`, and anything normalized to `TestNormalizationAgainstNetBox`;
   - a bound that only NetBox's import enforces, not its API, needs `TighterBounds` and a test against the import, as `TestMappingPositionsAgainstNetBox` does.

5. **Read it from each format.** For devicetype-library (`internal/format/devicetypelib`):
   - add the list to `componentLists` with the keys NetBox's import form takes and the library's schema allows; keys the schema allows but the import ignores go in `Dropped`, read and checked but not kept;
   - a list NetBox's import reads into another object's field, as `port-mappings` goes into each front port's `rear_ports`, is read on its own and folded in once every list is read (`mappings.go`);
   - make a file invalid exactly where NetBox's import refuses it, and check each refusal against the import in the shell; a reference to another template is checked once every list is read (`references`);
   - add problem cases to `TestReadComponentProblems`, a seed to `FuzzReadDeviceType`, and run `go test -run TestReadLibrary -update` and review the golden file;
   - read the whole library with `go test ./internal/format/devicetypelib -run TestWholeLibrary -args -library "$PWD/.cache/devicetype-library"` (an absolute path): it must read with none invalid.

6. **Compare it.** The engine works from specs, so a new child collection needs no engine code, unless the engine refuses it: `checkChildren` refuses a child's reference to an object that stands on its own, and a collection with an endpoint below the object compared. Extending the engine needs an ADR. Either way, add cases to the engine's tests (`children_test.go`, `ports_test.go`) and to the fuzz oracle in `fuzz_test.go`, and run each fuzzer for a minute or two (`go test ./internal/compare -run '^$' -fuzz '^FuzzCompare$' -fuzztime 90s`).

7. **Read it from NetBox.** `compare` reads each collection of the device types it matches, a listing each (`readChildren` in `cmd/netbox-trueup-ai/compare.go`), asking for the spec's fields and nested collections (`fieldsQuery`), and `netbox check` checks every spec's endpoint (`readTypes`). A type that stands on its own needs `readNetBox` to read it, and `compare.Referenced` or `compare.Matched` to limit the listing to what the definitions need. Add the endpoint's `fields=` to `TestCompareText`'s check.

8. **Run the parity test.** In `TestCompareParity` (`cmd/netbox-trueup-ai`), NetBox's own import of the test library must compare as in step; then change one thing at a time in NetBox, each making exactly its difference: a field, a reference, a child added (still in step) and one deleted (one difference). Then sample the whole library by hand: `go test -tags integration -run TestCompareParity ./cmd/netbox-trueup-ai -args -parity-library "$PWD/.cache/devicetype-library" -parity-sample 400`.

9. **Measure it.** Compare the whole library against the compose NetBox (`make netbox-up`) with this branch's binary and `main`'s, timed with `/usr/bin/time -f "%e s, %M KB peak"`, in text and JSON. Report the time, the JSON's size and the peak memory in the pull request.

10. **Document it:** the roadmap step, the README of each package changed, [usage.md](../../../docs/usage.md) for what `list` and `compare` print, [architecture.md](../../../docs/architecture.md) where it lists object types, uniqueness rules or comparison rules, the status in [AGENTS.md](../../../AGENTS.md), and an ADR for any significant decision ([what counts](../../../docs/adr/README.md)). Cite the NetBox source you followed in the pull request.
