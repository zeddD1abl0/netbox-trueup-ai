---
name: netbox-parity
description: Find out how NetBox itself treats a model or field - its type, limits, defaults, uniqueness, normalization and API shape - from NetBox's source at the target release, and write or check a spec from it. Use before writing or changing a spec under internal/model, or any rule that has to match NetBox's behaviour, such as comparison or validation.
---

# Matching NetBox's behaviour

TrueUp follows NetBox's rules, not its own ([architecture.md](../../../docs/architecture.md#principles), principle 3). Every spec says how NetBox treats each field, and cites where NetBox says so ([ADR 0008](../../../docs/adr/0008-describe-object-types-with-specs.md)). Your memory of NetBox is likely out of date, and NetBox's schema leaves out much of what matters. Read the source, check what it says against a real NetBox, and let the tests hold you to it ([ADR 0023](../../../docs/adr/0023-check-specs-against-the-schema-with-plain-json.md)).

## Where things are

| Path | What it holds |
| --- | --- |
| `.cache/reference/netbox-4.7.2/netbox/` | NetBox's source at the target release. `make reference` fetches it. Read it with absolute paths, and don't `cd` into it. |
| `internal/model/spec.go` | What a spec can say: `Spec`, `Field`, `Ref`, `Collection`, `Unique`, `Source`. |
| `internal/model/check.go` | `model.Check`, the consistency rules every set of specs must pass. |
| `internal/model/normalize.go` | `Field.Normalize` and `Field.Validate`, which follow what a spec says. |
| `internal/model/dcim/` | dcim's specs, the generated `choices_gen.go`, the schema snapshot in `testdata/openapi.json`, and the tests. `prototypes_test.go` shows children, nested collections, trees and generic references. |
| `internal/model/spectest/` | The contract with the schema, and the check of citations against NetBox's source. |
| `internal/model/openapi/` | Reads the schema, trims snapshots, lists choices; `cmd/netbox-choices` generates them. |

## Where NetBox says what

| You need | Read |
| --- | --- |
| Fields, lengths, `blank`, `null`, defaults, collations | The model, such as `DeviceType` in `dcim/models/devices.py`, and its base classes: `PrimaryModel` and `OrganizationalModel` in `netbox/models/__init__.py`, the mixins in `netbox/models/mixins.py` and `dcim/models/mixins.py`. |
| Uniqueness | `unique=True` on fields, and `Meta.constraints`. A rule over a nullable field doesn't hold where it is null unless the constraint sets `nulls_distinct=False`: record that as the rule's `Gap`. `natural_sort` (`dcim/migrations/0197_natural_sort_collation.py`) is an ICU collation, deterministic, so uniqueness is still exact. A `Lower(...)` in a constraint makes it case-insensitive. |
| Rules that span fields | The model's `clean`, such as `DeviceType.clean`, `WeightMixin.clean`. |
| What the API takes and gives | The serializer, such as `DeviceTypeSerializer` in `dcim/api/serializers_/devicetypes.py`: declared fields override the model's (`u_height`'s `min_value` and `default`), `nested=True` reads as a brief object, `PrimaryKeyRelatedField` as a bare ID. Base classes are in `netbox/api/serializers/`. |
| Choice fields | `ChoiceField` in `netbox/api/fields.py`: written as the value, read as `{value, label}`, blank read as null. The choice sets are in `dcim/choices.py` and `netbox/choices.py`. A set with a `key` can be replaced or extended through `FIELD_CHOICES` (`ChoiceSetMeta` in `utilities/choices.py`): set `Choices.Configurable` to `<app>.<key>`, such as `dcim.Device.airflow`. |
| Children carried by a parent | The serializer's nested field and its `create` and `update`, such as `PortSerializer` in `dcim/api/serializers_/base.py` and `reconcile_port_mappings` in `dcim/utils.py`. |
| NetBox's own import | The import form, such as `DeviceTypeImportForm` in `dcim/forms/bulk_import.py`, and `BulkImportView._process_import_records` and `_save_object` in `netbox/views/generic/bulk_views.py`. |
| The schema | `ChoiceFieldFix` and `NetBoxAutoSchema` in `core/api/schema.py`, and `SPECTACULAR_SETTINGS` in `netbox/settings.py`. |

Django and Django REST Framework aren't in NetBox's repository, but they decide much of the behaviour. Read them in the disposable NetBox's container, which has NetBox's exact versions:

```sh
docker exec trueup-netbox-netbox-1 sh -c 'sed -n "/^class DecimalField/,/^class /p" /opt/netbox/venv/lib/python3*/site-packages/rest_framework/fields.py'
```

What they decide, as of NetBox 4.7.2 (Django 6.1, DRF 3.18, Python 3.14):

- DRF's `CharField` trims input with Python's `str.strip`, which counts U+001C to U+001F as white space as well as everything Go's `unicode.IsSpace` does. Django's `forms.CharField` strips the same way for the import.
- DRF's `DecimalField` refuses a value with more places or digits than allowed rather than round it, and gives values at the field's places, as JSON numbers.
- Django's form `CheckboxInput` reads a missing key as false, so NetBox's import gives a boolean the user left out `False`, whatever the model's default. `construct_instance` keeps the model's default for other fields left out, but a field that isn't `blank` is required by the form even when the model has a default.

## Steps

1. **Fetch the source:** `make reference`. Find the model, its base classes, its serializer, its import form and its choice sets, using the table above.

2. **Look at the schema** NetBox publishes for the endpoint. Start the disposable NetBox (`make netbox-up`), then:

   ```sh
   curl -s -H "Authorization: Bearer $(cat .cache/netbox/token)" 'http://127.0.0.1:8000/api/schema/?format=json' > /tmp/schema.json
   ```

   Read the response component (the list's `results`), the create component (`oneOf[0]` of the POST body) and the patch component. Trust the source over the schema, and note where they differ:
   - a decimal's places appear only as its bounds, `10^(max_digits - decimal_places)`, exclusive;
   - only a default the serializer declares appears, never a model's;
   - a nullable reference is an `allOf` with `nullable` beside it, not inside;
   - a choice field's values include `""` and `null` where it takes them;
   - a `ColorField` is a `oneOf` of the colour and a string of no characters.

3. **Check behaviour you're unsure of** against the disposable NetBox, never another. In NetBox's shell, run each probe in a transaction that rolls back, inside a function, since the interactive console needs a blank line after a block:

   ```sh
   docker exec -i trueup-netbox-netbox-1 /opt/netbox/venv/bin/python /opt/netbox/netbox/manage.py nbshell <<'EOF'
   from django.db import transaction
   from dcim.forms import DeviceTypeImportForm
   def probe():
       with transaction.atomic():
           f = DeviceTypeImportForm(data={...})
           print('RESULT', f.is_valid(), dict(f.errors), f.save(commit=False).is_full_depth if f.is_valid() else None)
           transaction.set_rollback(True)

   probe()
   EOF
   ```

   Behaviour you rely on belongs in an integration test too (step 6), not only in your notes.

4. **Write the spec**, in field order as the API gives them:
   - `Kind`, `MaxLength`, `Pattern` (no anchors), `Digits` and `Places`, `Min`, `Max` and `Step` from the model and serializer. A bound tighter than the schema's, which NetBox's model enforces beyond its API (a weight can't be negative: `_normalized_measurement` in `utilities/conversion.py`), needs the field's `Source` to say where.
   - `Required` as the create component's `required` says; `Nullable` from `null=True` or `allow_null`; `Blank` from `blank=True` or `allow_blank`.
   - `Default` for every writable field that isn't required: the model's default, or the serializer's, or blank or null. `ImportDefault` and `ImportRequired` where the import differs.
   - `Trim` on every string field; `BlankIsNull` on choice fields that read blank as null. A choice field's `Source` is its choice set, and its `Choices.Configurable` is `<app>.<key>` if the set has a `key`, empty if not: the `Sources` test checks both.
   - References: `Ref.Types`, `Shape` (`ShapeNested` for a brief object, `ShapeID` for a bare ID, `ShapeTypeAndID` for a generic reference in two fields, which must cite where NetBox lists its types), and `Within` for a reference to another child of an ancestor, such as the device type. Depend (`DependsOn`) on every type a reference outside the parent refers to, in sets too.
   - `Identity`, which must be one of the `Unique` rules and have no gap, and every `Unique` rule with its `Gap`: a rule over a nullable field always has one.
   - Children: a `Collection` with its `Key`, which one of the children's `Unique` rules must make unique within the parent, or a `Gap` saying where it isn't; `Nested` when the children have no endpoint and the parent carries them, with a `Source` for its key; `Shared` for fields a nested tree's children copy from their parent.
   - `Ignored`, with a reason, for every field of the API you leave out: read-only bookkeeping, and references to types without a spec.
   - Choice values come from `choices("<Component>", "<field>")`, generated, never typed in.
   - `Sources` and per-field `Source`: a path under `netbox/` and a class, function or `Class.method`.

5. **Bring the snapshot and choices along.** A new endpoint needs the snapshot refreshed, then the choices: `make schema` (Docker, about 5 minutes) runs the integration test with `-update` and then `make generate`. Review the diff of `testdata/openapi.json`: it is NetBox's API, as it is now.

6. **Test.**
   - `make test` runs `spectest.Run` in each spec package: `Consistent` (`model.Check`), `Snapshot` (the snapshot holds exactly the specs' endpoints), `Contract` (the specs agree with it), and `Sources` (every citation is in NetBox's source; skipped until `make reference`).
   - In the spec package's integration test, extend `TestNormalizationAgainstNetBox` and `TestValidationAgainstNetBox` for the new fields, and give `TestDefaults` a factory for each new object type: it fails for a spec it can't create. They write to the disposable NetBox through `netboxtest` and check NetBox's answers against `Default`, `Normalize` and `Validate`.
   - When a check passes on the first try, break the spec in a test, as `TestContractFinds` does, to see it fail.

7. **Cite** the NetBox source you followed in the PR description, and record anything the schema and the source disagreed on.
