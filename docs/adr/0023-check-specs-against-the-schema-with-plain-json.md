---
status: accepted
date: 2026-10-28
decision-makers: "@zeddD1abl0"
---

# Check specs against a snapshot of NetBox's OpenAPI schema, read with plain JSON decoding

## Context and problem

Each NetBox object type TrueUp handles has a spec written in Go ([ADR 0008](0008-describe-object-types-with-specs.md)), and a contract test checks every spec against the OpenAPI schema of the target NetBox, 4.7.2 for now ([ADR 0003](0003-initial-netbox-target.md)): the fields exist, and writability, types, nullability and choice values agree. [tooling.md §3.1](../tooling.md#31-netbox-access-and-comparison) listed kin-openapi for reading the schema as a spike, against plain JSON decoding. Roadmap step 8 builds the first specs, for manufacturers and device types, and the contract test.

Three questions come with it. What reads the schema? Where does the test get the schema from: the disposable NetBox of `internal/netboxtest`, which takes about 4 minutes to start, or a copy committed to the repository? And should parts of specs, such as choice values, be generated from the schema?

NetBox 4.7.2 publishes its schema at `/api/schema/`: OpenAPI 3.0.3, built by drf-spectacular with request and response components split (`SPECTACULAR_SETTINGS` in `netbox/netbox/settings.py`, `NetBoxAutoSchema` in `netbox/core/api/schema.py`). In JSON it is 14.7 MB, with 322 paths and 1,111 components, and NetBox takes about 4 seconds to generate it.

## Decision drivers

- Read NetBox's schema correctly, including drf-spectacular's habits, such as the `allOf` it wraps a nullable reference in.
- Prefer the standard library, and add no dependency without a reason.
- Fast unit tests that need no Docker, so that whoever writes a spec sees a mistake at once.
- A record, reviewed in pull requests, of how NetBox's API changes from one release to the next.
- Specs that can't miss a choice value NetBox adds.

## Considered options

1. Plain `encoding/json` into a few structs of our own, with a trimmed snapshot committed and checked against the live schema, and choice values generated from the snapshot
2. kin-openapi, with the same snapshot and generation
3. Either reader against the live schema only
4. Either reader with the whole schema committed

## Decision

Chosen option: "Plain `encoding/json`, a trimmed snapshot checked against the live schema, and generated choice values".

- **Reading.** `internal/model/openapi` decodes the schema into structs that hold the keywords NetBox's schema uses, and follows its `$ref`s to components itself. It finds an endpoint's components the way NetBox's API uses them: the objects a list's GET gives, what its POST takes, which is one object or a list, and what a PATCH of one object takes.
- **Snapshot.** Each spec package keeps a snapshot in `testdata/openapi.json`: the schema trimmed to the endpoints its specs have, with only the operations the contract reads and the components they refer to, written as indented JSON with keys in order and numbers as NetBox wrote them. For manufacturers and device types, with the prototype component templates of step 8, it is 107 KB. `make test` checks specs against it, with no Docker, and checks that it holds exactly the specs' endpoints.
- **Live check.** An integration test in each spec package fetches the schema from the disposable NetBox, checks the specs against it, and fails if trimming it doesn't give the committed snapshot byte for byte. `make schema` runs that test with `-update` to rewrite the snapshot, and then regenerates the choices. A new NetBox release thus shows up as a diff of the snapshot, for review.
- **Contract.** `internal/model/spectest` checks, for each field a spec describes: that the API gives it, writes it when creating and patching, or doesn't, as the spec says; its kind; whether it is required, nullable and blank; its length and pattern; a number's bounds, which for a decimal also give its digits; a choice field's values; a reference's target and shape; and any default the schema states. Every field of the API must be described or ignored with a reason, so a field NetBox adds fails the test.
- **What the schema can't say.** The schema gives a decimal's places only through its bounds, a default only where the serializer declares one, and no uniqueness rules, dependencies, import defaults or normalization. Specs take those from NetBox's source, and cite it. Integration tests check defaults, normalization and validation against NetBox itself, by creating objects through its REST API.
- **Generation.** `make generate` writes each spec package's choice values from its snapshot into `choices_gen.go`, with `netbox-choices`, and `make generate-check`, part of `make check`, fails when they differ. Specs refer to them by component and field, such as `DeviceType.airflow`.

### Consequences

- Good, because there is no new dependency, and the reader, with trimming and choice listing, is about 330 lines of code of our own, tested against the real schema.
- Good, because unit tests check every spec against NetBox's schema in a fraction of a second, without Docker.
- Good, because a NetBox upgrade shows up as a reviewable change to the snapshots, and to the generated choices.
- Bad, because the snapshot must be refreshed whenever a spec gains an endpoint, which needs Docker and about 5 minutes.
- Bad, because the reader understands only the keywords NetBox's schema uses today. A schema that starts using others fails the contract test, rather than being read wrongly.
- Bad, because an administrator can change some choice values through NetBox's `FIELD_CHOICES` setting, so generated values are only NetBox's defaults. Specs mark those fields (`Choices.Configurable`).

## Pros and cons of the options

### Plain `encoding/json`

- Good, because it adds nothing to `go.mod`.
- Good, because, in a prototype that loaded NetBox 4.7.2's schema and summarized four endpoints, it took 80 to 200 ms, allocated 9.6 MB and peaked at 27 MB.
- Bad, because the code is ours: the prototype was 150 lines against kin-openapi's 91, and its one bug was dropping the `nullable` that drf-spectacular puts beside an `allOf`.

### kin-openapi

- Good, because it gives a typed model of OpenAPI and resolves references.
- Bad, because v0.151.0, the latest release at least 7 days old, adds six modules: kin-openapi, go-openapi's jsonpointer and swag/jsonname, oasdiff's yaml and yaml3, and santhosh-tekuri's jsonschema/v6.
- Bad, because, in the same prototype, it took 1.1 s, allocated 438 MB and peaked at 166 MB, and the test binary grew from 3.2 to 11.4 MB.
- Bad, because its own validation rejects NetBox's schema, whose `servers` list has an empty URL, so we would skip validation anyway.

### The live schema only

- Good, because nothing is committed.
- Bad, because every spec change then needs Docker and a NetBox that takes 4 minutes to start before it is checked at all.
- Bad, because nothing records how NetBox's API changed between releases.

### The whole schema committed

- Good, because a new spec needs no refresh.
- Bad, because 14.7 MB, or about 5 MB minified, would change with every NetBox release, mostly in parts TrueUp doesn't use.

## More information

- The spike's prototype and measurements are summarized in the pull request that made this decision.
- How to write a spec, and check it against NetBox's source and schema, is in the `netbox-parity` project skill (`.claude/skills/netbox-parity/SKILL.md`).
- drf-spectacular's ChoiceField extension, which gives a choice field as `{value, label}` and lists the blank and null values among its choices: `ChoiceFieldFix` in `netbox/core/api/schema.py`.
