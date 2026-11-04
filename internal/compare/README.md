# The comparison engine

`compare` matches definitions to the objects NetBox holds, by NetBox's own uniqueness rules, and compares each definition with the object it matches, field by field, side by side ([ADR 0025](../../docs/adr/0025-match-by-netbox-uniqueness-rules.md), [architecture.md](../../docs/architecture.md#comparison-engine)). It does no I/O and reads no clock: `cmd/netbox-trueup-ai`'s `compare` reads the sources and NetBox, passes their objects in, and records the results.

```go
results, err := compare.Compare(dcim.Specs(), definitions, observed)
```

- `definitions` are a format's definitions, each with the name of its source.
- `observed` are NetBox's objects, each read from NetBox's API through its spec by `Spec.DecodeAPI` in `internal/model`. They must include every object a definition could match or collide with: every manufacturer, and the device types of each manufacturer a definition names, since both of a device type's rules include its manufacturer.
- `Compare` returns a result for each definition, in the same order: its outcome, `candidate`, `absent` or `conflict`, the collisions behind a conflict, the references NetBox has nothing, or more than one object, for, and the columns and rows of the side-by-side table. Each result encodes as JSON the way `compare --output json` prints it, and the database records it so.

What it settles, and where each rule comes from, is in ADR 0025: values compare exactly, case and white space included, as NetBox's constraints compare them; a value the format derived counts only where TrueUp would create the object; only the fields a definition sets are managed.

Definitions come from sources and objects from NetBox, so both are untrusted. `Compare` fails, rather than guess, on an object or a definition of a type without a spec, or that doesn't hold its fields as its spec describes them (`check.go`): a field it doesn't describe, a value of another kind, a NetBox object without one of its fields, a reference to a type the field can't refer to, or a definition whose identity disagrees with its fields. It fails on an object NetBox listed twice too. Two NetBox objects that break one of NetBox's own uniqueness rules are a conflict, and so is a device type whose manufacturer they both could be.

## Tests

- `compare_test.go` holds a case for each matching rule, the rows of the side-by-side table, and the JSON.
- `FuzzCompare` builds definitions and NetBox objects from a few names that differ in case and white space, so that they often collide, and holds `Compare` to a plain reading of NetBox's rules, `oracle`, and to results that don't depend on NetBox's order. NetBox's objects need not keep NetBox's rules, since an answer can't be trusted to.
- `FuzzCompareWithItself` checks that a definition of what NetBox holds is in step with it, and that changing one field makes exactly that difference.

`make test` runs the fuzz tests' seeds. To fuzz:

```sh
go test ./internal/compare -run '^$' -fuzz '^FuzzCompare$' -fuzztime 5m
go test ./internal/compare -run '^$' -fuzz FuzzCompareWithItself -fuzztime 5m
```

The parity test, `TestCompareParity` in `cmd/netbox-trueup-ai`'s integration tests, imports the test library through NetBox's own YAML import into the disposable NetBox, and checks that every definition is in step, and that changing one field in NetBox makes exactly that difference. To run it against a sample of a whole checkout of the library instead, give it absolute paths:

```sh
go test -tags integration ./cmd/netbox-trueup-ai -run TestCompareParity -v \
  -parity-library "$PWD/.cache/devicetype-library" -parity-sample 300
```
