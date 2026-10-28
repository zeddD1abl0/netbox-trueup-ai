# The object model

TrueUp handles NetBox objects generically: an object is an object type and the values of its fields, and what each object type means lives in its spec ([ADR 0008](../../docs/adr/0008-describe-object-types-with-specs.md), [architecture.md](../../docs/architecture.md#object-model-and-specs)). None of this does I/O.

| Package | What it holds |
| --- | --- |
| `model` | `Object` and its values (`String`, `Integer`, `Decimal`, `Boolean`, `Date`, `Choice`, `Reference`, `Set`, `Children`, `Null`), `Spec`, `Field.Normalize` and `Field.Validate`, and `Check`, which checks that specs are consistent. |
| `model/dcim` | The specs of dcim's object types: manufacturers and device types. Component templates follow in roadmap steps 11 and 12; `prototypes_test.go` sketches them. |
| `model/openapi` | Reads NetBox's OpenAPI schema, trims it to a snapshot, and lists its choice values. `cmd/netbox-choices` generates a spec package's `choices_gen.go` from its snapshot. |
| `model/spectest` | The tests every spec package runs: consistency, the contract with the schema, and citations of NetBox's source ([ADR 0023](../../docs/adr/0023-check-specs-against-the-schema-with-plain-json.md)). |

## Specs and NetBox's schema

Each spec package keeps a snapshot of NetBox's schema in `testdata/openapi.json`, trimmed to its specs' endpoints. Its choice values are generated from it into `choices_gen.go`.

- `make test` checks the specs against the snapshot, with no Docker. With NetBox's source in `.cache/reference` (`make reference`), it also checks that every citation is there.
- `make test-integration` starts a disposable NetBox and checks that the snapshot is its schema, and that NetBox's defaults, trimming and validation are those the specs describe.
- `make schema` rewrites the snapshots from a disposable NetBox, then runs `make generate`. Run it after adding an endpoint, or when the target NetBox changes, and review the diff: it is how NetBox's API changed.
- `make generate` regenerates the choice values, and `make generate-check` checks them. Never edit `choices_gen.go` by hand.

To write or change a spec, follow the `netbox-parity` project skill in [`.claude/skills/netbox-parity/SKILL.md`](../../.claude/skills/netbox-parity/SKILL.md): it says where NetBox's source holds each rule, and how to check it.
