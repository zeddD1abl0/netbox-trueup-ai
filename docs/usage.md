# Using TrueUp from the command line

This guide takes you from a configuration file to a comparison of [devicetype-library](https://github.com/netbox-community/devicetype-library) with your NetBox: which of the library's manufacturers and device types NetBox already has, which it lacks, and how those it has differ, field by field and component by component.

So far TrueUp only reads. It fetches the library, reads its device types as NetBox's own YAML import would, and compares them with what NetBox holds. Nothing writes to NetBox yet: importing and syncing come in milestone 2 ([roadmap](roadmap.md)). Of a device type's component templates, its console ports, console server ports, power ports, power outlets, interfaces, rear ports, front ports with their mappings to rear ports, module bays and device bays are compared. Inventory items aren't: NetBox has deprecated them in favour of modules, and `list` and `compare` warn about a file that has any, or refuse it if they aren't a list of mappings, as NetBox's import does ([ADR 0028](adr/0028-no-inventory-item-templates.md)).

Every setting mentioned here is described in [configuration.md](configuration.md), and every command's `--help` lists its flags and exit codes.

## Before you start

You need:

- the `netbox-trueup-ai` binary. From a clone of this repository, `make build` puts it in `bin/`;
- a NetBox 4.7, and a v2 API token for a user with the view permission on manufacturers and device types. The token needn't be write-enabled;
- a directory TrueUp can keep its data in: a cache of each source, and a SQLite database, unless `database.url` names a PostgreSQL one.

Write a config file, such as `/etc/netbox-trueup-ai/config.yaml`, or anywhere you name with `--config`:

```yaml
data_dir: /var/lib/netbox-trueup-ai
netbox:
  url: https://netbox.example.com
  token_file: /etc/netbox-trueup-ai/netbox-token
sources:
  - name: devicetype-library
    kind: git
    url: https://github.com/netbox-community/devicetype-library.git
    ref: master
    format: devicetype-library
    # Leaves out the library's elevation images: a 64 MB download, not 490 MB.
    max_file_size: 128KiB
```

The token file holds the token alone, and should be readable by you only. To read only some manufacturers, give the source `include` patterns, such as `["device-types/Dell/**"]`; the library's schemas are added for you ([Patterns](configuration.md#patterns)).

## Check NetBox and fetch the library

```sh
netbox-trueup-ai netbox check
netbox-trueup-ai source fetch
```

`netbox check` connects, checks NetBox's release and the token, and lists what the token can read ([Connecting to NetBox](configuration.md#connecting-to-netbox)). `source fetch` downloads the library's latest commit into the cache, records the fetch in the database, and loads the definitions the library's files hold into the database, which takes a few seconds ([Fetching sources](configuration.md#fetching-sources)). Fetch again whenever you want the library's latest version: `list` and `compare` read the definitions of the version last fetched from the database, unless you give them `--fetch`.

## List what the library defines

```sh
netbox-trueup-ai list
```

`list` prints each source's summary, then each manufacturer followed by its device types, then warnings and invalid files. It reads them from the database, not NetBox, in about a second for the whole library. A manufacturer is derived from each name the library's device types give, since the library has no files for manufacturers, with the slug NetBox's web UI would make from the name, marked `(derived)`. TrueUp uses a derived slug only to create a manufacturer, never to change one NetBox has.

A file that fails is reported with its path, line, column, field and reason, and skipped, such as a value NetBox would refuse. [Listing definitions](configuration.md#listing-definitions) describes what `list` reads, how it validates each file, and its JSON.

To find a device type, search the manufacturer, model and part number, ignoring case:

```sh
netbox-trueup-ai list --search r640
```

```text
SOURCE              REF     VERSION                                   FILES  INVALID  WARNINGS  MANUFACTURERS  DEVICE TYPES
devicetype-library  master  193473cbce8d8ce8171779dc9d76b9fd22ea8e7c  13374  0        4         0              1

SOURCE              TYPE         MANUFACTURER  MODEL           SLUG                 FROM
devicetype-library  device type  Dell          PowerEdge R640  dell-poweredge-r640  device-types/Dell/PowerEdge-R640.yaml

Warnings:
  devicetype-library: manufacturers "GL-iNet" and "GL.iNet" would both get the slug "gl-inet", which NetBox gives to one manufacturer only: ...
  ...
```

The search takes a few hundredths of a second, since it reads only the database, and the warnings and invalid files are the whole source's, as without one. A file's name needn't match its model: `device-types/A10/TH4440.yaml` defines the "Thunder 4440 ADC", which `--search thunder` finds.

## Compare with NetBox

```sh
netbox-trueup-ai compare
```

```text
NetBox:  https://netbox.example.com, release 4.7.2

SOURCE              REF     VERSION                                   COMPARED  IN STEP  DIFFERING  ABSENT  CONFLICTS  INVALID  WARNINGS
devicetype-library  master  193473cbce8d8ce8171779dc9d76b9fd22ea8e7c  7         5        1          0       1          0        4

7 objects compared: 5 in step, 1 differing, 0 absent, 1 in conflict.

13774 definitions NetBox hasn't got weren't compared: --all compares every definition, and --manufacturer or --model those named.

OUTCOME    TYPE          MANUFACTURER  MODEL           NETBOX  DIFFERENCES  SOURCE              FROM
conflict   manufacturer  NETGEAR       -               -       -            devicetype-library  1 file
candidate  device type   Dell          PowerEdge R640  #3      1            devicetype-library  device-types/Dell/PowerEdge-R640.yaml

Conflicts:
  manufacturer "NETGEAR": devicetype-library (1 file): its slug matches NetBox's manufacturer #3, "Netgear"

Differences:
  device type Dell "PowerEdge R640", NetBox #3, in devicetype-library (device-types/Dell/PowerEdge-R640.yaml):
    FIELD     devicetype-library  NETBOX
    u_height  1                   2

Warnings:
  devicetype-library: manufacturers "NETGEAR" and "Netgear" would both get the slug "netgear", ...
```

By default `compare` compares what NetBox has: the definitions that match one of its objects, or would collide with one. The summary counts each source's definitions by outcome, then the objects they define, and says how many definitions NetBox hasn't got, which aren't compared. The table below it lists every object compared that isn't in step with NetBox, with the source and file of each of its definitions; those in step are only counted. Then come the conflicts, the differences of each object, with a column for each definition, and the warnings and invalid files that `list` would show. With one source, each object has one definition; [Several sources](#several-sources) shows more.

### Outcomes

Each definition is matched with NetBox's objects by NetBox's own uniqueness rules: a manufacturer's name, and its slug; a device type's manufacturer and model, and its manufacturer and slug ([ADR 0025](adr/0025-match-by-netbox-uniqueness-rules.md)). One of those rules is the definition's identity: a manufacturer's name, and a device type's manufacturer and model.

| Outcome | Means | Importing it would |
| --- | --- | --- |
| candidate | Its identity matches one NetBox object, and no other rule matches a different object. | link it to that object |
| absent | Nothing in NetBox matches it. | create it |
| conflict | Another rule matches an object that isn't its match, so NetBox couldn't hold both. | wait for a person to decide |

A candidate with no differences is **in step**. Some conflicts you will meet:

- A device type whose model matches one device type, and whose slug another: two device types of NetBox's are the library's one.
- A device type whose slug alone matches: often a device type renamed in the library, or in NetBox.
- A manufacturer whose name NetBox lacks, but whose derived slug another has: the library's `NETGEAR` beside NetBox's `Netgear`. Settle it in NetBox, by giving one of them another slug, or in the library.

Values are compared exactly, case and white space included, as NetBox's database compares them: `Dell` and `dell` are different manufacturers, to NetBox's import as to TrueUp. A device type whose manufacturer NetBox lacks is absent, and its JSON says what the manufacturer's own definition found. A NetBox that holds two objects with the same name or slug, which its own rules forbid, can't say which one a definition is: the definition is a conflict, and so are the device types of a manufacturer NetBox holds twice.

### Differences

A candidate's fields are compared as NetBox stores them, so that only real differences show: `u_height: 1` equals NetBox's `1.0`, text is compared after trimming white space at both ends, as NetBox trims it, and a blank choice equals NetBox's null.

Only the fields a definition sets are compared. devicetype-library leaves out fields such as `end_of_life` and `exclude_from_utilization`, and many of its files leave out `airflow` or `part_number`: whatever NetBox holds there isn't a difference, since TrueUp won't manage those fields ([ADR 0006](adr/0006-manage-only-linked-objects.md)). A derived value, such as a manufacturer's slug, isn't a difference either: TrueUp uses it only to create the manufacturer. JSON output shows every field side by side, these included.

### Component templates

A device type's component templates are matched with NetBox's by name, within the device type, exactly, and compared field by field as the device type's own fields are ([ADR 0026](adr/0026-compare-children-by-their-key.md)):

- A template the library lists and NetBox hasn't got is one difference, shown as `present` and `missing`, however many fields it sets.
- A template only NetBox has, such as one added by hand, is listed under "Only in NetBox", but isn't a difference: TrueUp never removes one, so the device type can still be in step.
- A field the library leaves out, such as an interface's `enabled`, isn't managed, as for a device type.
- A power outlet's `power_port` compares by the name of the power port it refers to.
- A front port's mappings, which the library lists apart as `port-mappings`, are compared within the front port, each by its position on it, as NetBox keeps them ([ADR 0027](adr/0027-nest-port-mappings-in-their-front-port.md)). A mapping's rear port compares by its name, and its `rear_port_position` as a field. A front port NetBox hasn't got is one difference, its mappings and all; a mapping only NetBox has, on a front port the library lists, is listed under "Only in NetBox", but isn't a difference.

A difference names its template by its list and name, such as `interfaces["eth0"].type`, and a mapping by its front port and position, such as `front_ports["FP1"].rear_ports[1].rear_port`. TrueUp reads the library's lists as NetBox's own import does: an interface's `bridge`, which NetBox's import ignores, isn't compared, and neither is a console or rear port's `_is_power_source`, which is for the library's own tests. A file is invalid, since NetBox's import would refuse it, if it names a template twice, has a power outlet whose power port the device type hasn't got, or a port mapping whose front or rear port it hasn't got, maps to a rear port position beyond the rear port's positions, or maps a front or rear port's position that another mapping maps too. A front port position beyond the front port's positions is no error, to NetBox's import or to TrueUp.

### Several sources

Two sources can define the same device type: devicetype-library, and a repository of your own with the device types you have changed or added. `compare` groups their definitions into objects, by type and identity as NetBox would store them: two device types with the same manufacturer and model, white space at the ends aside, are one object ([ADR 0032](adr/0032-compare-definitions-across-sources-as-objects.md)).

```text
SOURCE              REF     VERSION                                   COMPARED  IN STEP  DIFFERING  ABSENT  CONFLICTS  INVALID  WARNINGS
devicetype-library  master  193473cbce8d8ce8171779dc9d76b9fd22ea8e7c  7         5        1          0       1          0        4
our-types           main    5d41402abc4b2a76b9719d911017c592ae31a2f1  2         1        1          0       0          0        0

7 objects compared: 5 in step, 1 differing, 0 absent, 1 in conflict.

13775 definitions NetBox hasn't got weren't compared: --all compares every definition, and --manufacturer or --model those named.

OUTCOME    TYPE          MANUFACTURER  MODEL           NETBOX  DIFFERENCES  SOURCE              FROM
conflict   manufacturer  NETGEAR       -               -       -            devicetype-library  1 file
candidate  device type   Dell          PowerEdge R640  #3      2            devicetype-library  device-types/Dell/PowerEdge-R640.yaml
                                                                            our-types           device-types/Dell/R640.yaml

Conflicts:
  manufacturer "NETGEAR": devicetype-library (1 file): its slug matches NetBox's manufacturer #3, "Netgear"

Differences:
  device type Dell "PowerEdge R640", NetBox #3, in devicetype-library (device-types/Dell/PowerEdge-R640.yaml) and our-types (device-types/Dell/R640.yaml):
    FIELD                devicetype-library  our-types  NETBOX
    u_height             1                   2          2
    interfaces["iDRAC"]  missing             present    missing
```

- Each definition is compared with NetBox on its own, and keeps its outcome: the sources' counts count definitions. Here the library's R640 differs from NetBox in its height, and our-types' in its iDRAC interface.
- Each object has an outcome too, which the objects' line counts and `--exit-code` uses: a conflict if one of its definitions is, if a source defines it twice, or if it clashes, and otherwise its definitions' outcome. It is in step when NetBox and every definition agree, so a difference between two sources is a difference of the object, even where NetBox agrees with one of them.
- An object's table has a column for each definition, sources in the configuration's order, then NetBox's. A definition that doesn't set a field leaves its cell `not set`; one without a template shows it `missing`, or `-` where it hasn't the template's front port either.
- A source that defines one object twice makes it a conflict, with a column for each file, such as `our-types (1)` and `our-types (2)`.
- Two objects clash when importing both would leave NetBox with two objects with the same name or slug that NetBox hasn't got yet, such as two models one manufacturer defines with one slug: each is a conflict. A manufacturer's derived slug counts only where NetBox hasn't got the manufacturer, since TrueUp would only create it. Clashes are looked for among the objects compared: with `--all` against an empty NetBox, the library's NETGEAR and Netgear, and three other pairs, are conflicts.
- By default, an object NetBox has is compared with every definition of it: a definition whose unique values match nothing in NetBox, since its slug differs from the one another source's definition gives, is compared, recorded and counted as absent.

### Choosing what to compare

- By default, `compare` compares what NetBox has: candidates and conflicts. A definition NetBox hasn't got is counted, but not compared, listed or recorded. It is matched with NetBox by its unique values alone, the fields NetBox keeps unique, which TrueUp keeps beside each definition, and only what is compared is read whole ([ADR 0030](adr/0030-compare-what-netbox-has-by-default.md)).
- `compare devicetype-library` compares only the sources named.
- `--manufacturer NAME` compares that manufacturer and its device types, absent included. Repeat it for more. TrueUp then reads only those manufacturers' device types from NetBox.
- `--model MODEL` compares the device types with that model, in every manufacturer, absent included, and their manufacturers, from every source that defines them, which importing them needs. Repeat it for more. With `--manufacturer`, it compares only those manufacturers' device types with that model, and the manufacturers.
- A name matches as NetBox would store it: without white space at either end, and otherwise exactly, case included. A name no source defines gets a warning, and the exit code 4, so that a mistyped name doesn't pass for a comparison in step.
- `--all` compares every definition, as `compare` did before step 14 of the roadmap. It can't be given with `--manufacturer` or `--model`.
- `--fetch` fetches each source first, as `source fetch` does.

### Exit codes

| Code | Means |
| --- | --- |
| 0 | Every source was read and its comparisons recorded, and every file is valid, whatever the outcomes. |
| 1 | The command couldn't run: NetBox can't be reached, runs another release, refuses the token, or the token can't read manufacturers, device types or the component templates compared, or the database can't be opened or written. |
| 2 | A name given isn't a configured source, `--all` is given with `--manufacturer` or `--model`, or either of them is given white space alone. |
| 3 | The configuration is invalid, or `netbox.url` or the token isn't set. |
| 4 | Some sources couldn't be read, or their comparisons recorded, or some files are invalid, or no source defines a manufacturer `--manufacturer` names or a model `--model` names. The rest is compared and printed as usual. |
| 5 | Only with `--exit-code`: everything was read, and some objects compared are absent, in conflict or differ, from NetBox or between sources. |

`--exit-code` counts only what was compared, so by default it suits a scheduled job that should fail when what NetBox has falls behind the library; with `--all`, it fails while NetBox lacks anything the library defines.

### Permissions

TrueUp sees what the token's permissions let it see. A token that can't read manufacturers, device types or one of the kinds of component template at all fails the command, naming what it can't read; `netbox check` lists them all. A permission with constraints, such as one limited to some manufacturers, can't be detected: what it hides shows as absent, or for a template as missing.

### Recorded comparisons

Each run records the comparison of each definition it compared with NetBox, alone, in the database, as the latest of that definition, replacing the one before: when it was made, against which version of the source and which NetBox, and the result, as `--output json` prints it. By default, and with `--all`, a run's comparisons replace all of the source's, so the comparisons of definitions NetBox no longer has go; with `--manufacturer` or `--model`, the other definitions keep the comparisons recorded before. Loading a version of the source removes the comparisons of the definitions it no longer holds, and of files that no longer read, which define nothing: the invalid file is reported instead, and its comparison returns when it reads again. A comparison is recorded only against the version of the source that the database holds, as the database loaded it: if the source was fetched or loaded again while it was compared, nothing is recorded for it, and `compare` says so and exits with 4. A definition compared because another source's definition of its object matches NetBox is recorded too, so whether a definition has a record after a default run can depend on the sources compared with it, though what the record says doesn't. An object's comparison isn't recorded: it is built again by merging its definitions' records, as `compare` merges them. The web UI will read these records; for now they are in the `comparisons` table.

## See who changed what

Every action that changes what TrueUp manages, what NetBox holds, or what sources TrueUp reads is recorded in an audit log in the database, never changed or deleted ([ADR 0034](adr/0034-audit-what-changes-what-trueup-manages.md)). So far that is what fetching changes, with `source fetch`, or with `list` or `compare` and `--fetch`: a source added, its kind, URL or ref changed, a new version fetched, and a source no longer configured removed. Fetching a version TrueUp holds, loading, listing and comparing change nothing that is audited. A command that can't tell who runs it fetches nothing. Importing, planning and applying will be recorded as they arrive.

```sh
netbox-trueup-ai audit
```

lists the latest 100 events, oldest first, each with who acted, when, and what changed:

```text
#12  2026-11-23T09:30:01Z  source.new_version  succeeded
    by jordan (uid 1000), on ops1, at the command line
    about source devicetype-library
    before: 3f2a1b9c0d4e
    after: 9c41e07a52bd
    ref: master
```

At the command line, who acted is the operating system user running the command, found by its user ID, never from `$USER`; a user ID the system's user database lacks, as in some containers or for users from a directory such as LDAP, is shown alone. A command run through sudo records the user who ran sudo too, by name and user ID. Narrow the list with `--since` and `--until`, each an RFC 3339 time or a duration back from now (`90m`, `24h`, `7d`), `--actor NAME`, which matches a user ID too, and the name or ID of the user who ran sudo, `--action ACTION`, repeatable, and `--source NAME`; `--limit N` lists more or fewer of the latest, and `--limit 0` all of them. `--output json` prints `{"events": [...], "more": ...}`, each event with its `actor` as an object, and `more` true when `--limit` left older ones out.

## Reading the JSON

With `--output json`, `compare` prints one document whose fields scripts can rely on ([ADR 0018](adr/0018-cli-conventions.md)):

- `ok`: whether every source was read and recorded, and every file is valid;
- `scope`: what was compared: `netbox`, what NetBox has, by default; `selection`, what `--manufacturer` and `--model` name; or `all`;
- `netbox`: its `url` and `version`; and `compared_at`, in UTC;
- `sources`: for each, `name`, `format`, `ref`, `version`, `ok`, `error`, `files`, `invalid_files`, `definitions` and `compared`, of which, by default, `definitions` − `compared` is how many NetBox hasn't got, the counts of the definitions compared by their own outcome, `in_step`, `differing`, `absent` and `conflicts`, and `problems`, as `list` gives them;
- `counts`: the objects compared, `compared`, and by their outcome, `in_step`, `differing`, `absent` and `conflicts`;
- `objects`: each object compared, with every definition of it, in the order of their first definitions, sources in the configuration's order;
- `warnings`, each with a `message`.

Each object has:

- `type` and `identity`, as NetBox would store it, such as `{"manufacturer": {"name": "Dell"}, "model": "PowerEdge R640"}`;
- `outcome`, `in_step`, `netbox_id` (null unless the identity matches) and `differences`, the count of rows that differ and are managed, between definitions as well as with NetBox;
- `collisions`, every definition's, each once, each with its `rule`, such as `["manufacturer", "slug"]`, and the NetBox object's `netbox_id` and `identity`, and its `path`: empty for an object such as a device type, and the template's path, such as `["interfaces", {"name": "eth0"}]`, for one of two templates NetBox holds under one name. A port mapping's collision has a null `netbox_id`, since NetBox gives mappings no ID;
- `unresolved`: references to objects NetBox lacks, each once, each with its `field`, `type`, `key`, `netbox_ids`, the objects that all have the key when NetBox holds more than one, and the `outcome` of the object it refers to, which is compared with it, or null;
- `duplicated_in`: the sources that define the object more than once;
- `clashes`: each with its `rule`, the `values` the object's definitions in `columns`, by their index, and the definitions in `with`, each with the other object's `type` and `identity` and the definition's `source`, `path` and `derived_from`, would all leave NetBox holding;
- `columns`: the versions compared, each definition first, with its `source`, `path` (null for a derived manufacturer), `derived_from` and `unmapped`, the lists of children the file holds that TrueUp is to read but doesn't yet, which for devicetype-library is always empty (inventory items, which TrueUp doesn't support, get a warning instead), and its own comparison with NetBox's `outcome`, `in_step`, `differences` and `collisions`; then NetBox's object, with its `netbox_id`, when there is one;
- `rows`: each field any version sets, with its `path`, a list of steps, one `cell` per column, `{"value": ...}` or `{}` where that version doesn't set the field, `differs`, `managed`, where some definition manages the field, and `derived`, where some definition's value was derived, and `netbox_id`, which is null but on a template's row. A port mapping's row has none, since NetBox gives mappings no ID.

The database records each definition's comparison as an object of that definition alone prints, but without `duplicated_in` and `clashes`, and without its column's own comparison, which the record holds at its top; its `identity` is as the definition gives it, and an unresolved reference's `outcome` is that of the same source's definition of what it refers to.

A path is a list of steps: a field's or a list's name, as a string, and a template's key, as an object. So a device type's own field is `["u_height"]`, an interface is `["interfaces", {"name": "eth0"}]`, and its type `["interfaces", {"name": "eth0"}, "type"]`. A front port's mapping at position 1 is `["front_ports", {"name": "FP1"}, "rear_ports", {"position": 1}]`, and its rear port `["front_ports", {"name": "FP1"}, "rear_ports", {"position": 1}, "rear_port"]`. A template's own row, whose path ends at its key, says whether each version has it, as `true` or `false`, with NetBox's template's `netbox_id`; it is managed when the library lists the template. Rows for a template's fields follow it, for each field the library sets or NetBox holds something other than its default, and then its mappings' rows, if it is a front port. Where NetBox hasn't got the front port, its mappings' rows leave NetBox's cell `{}`.

With more than one definition, NetBox's cell isn't the second: find NetBox's column by its `kind`, as the questions below do.

Some questions it answers, with [jq](https://jqlang.org):

```sh
# The device types NetBox lacks, as manufacturer and model: only --all, or
# a selection, compares those.
netbox-trueup-ai compare --all -o json | jq -r '.objects[]
  | select(.type == "dcim.devicetype" and .outcome == "absent")
  | "\(.identity.manufacturer.name)\t\(.identity.model)"'

# Every field and template that differs, with each source's value and
# NetBox's, a template's path written as interfaces[eth0].type.
netbox-trueup-ai compare -o json | jq -c 'def text: map(if type == "object" then "[\([.[] | tostring] | join(","))]" else . end) | join(".") | gsub("\\.\\["; "[");
  .objects[] | select(.differences > 0) | .columns as $columns
  | ($columns | map(.kind) | index("netbox")) as $netbox
  | {model: .identity.model, netbox_id, fields: [.rows[] | select(.differs and .managed) | . as $row
  | {field: (.path | text),
     sources: [range($columns | length) | select($columns[.].kind == "definition") | {source: $columns[.].source, value: $row.cells[.].value}],
     netbox: (if $netbox then .cells[$netbox].value else null end)}]}'

# The templates and port mappings NetBox lacks, by device type.
netbox-trueup-ai compare -o json | jq -c 'def text: map(if type == "object" then "[\([.[] | tostring] | join(","))]" else . end) | join(".") | gsub("\\.\\["; "[");
  .objects[] | select(.netbox_id != null) | (.columns | map(.kind) | index("netbox")) as $netbox
  | {model: .identity.model, missing: [.rows[] | select((.path[-1] | type == "object") and .managed and .cells[$netbox].value == false)
  | .path | text]} | select(.missing != [])'

# The templates and port mappings only NetBox has, which TrueUp leaves
# alone, with templates' IDs.
netbox-trueup-ai compare -o json | jq -c 'def text: map(if type == "object" then "[\([.[] | tostring] | join(","))]" else . end) | join(".") | gsub("\\.\\["; "[");
  .objects[] | .identity.model as $model | .rows[]
  | select((.path[-1] | type == "object") and .differs and (.managed | not))
  | {model: $model, path: (.path | text), netbox_id}'

# The conflicts, with what each collides with in NetBox, the sources that
# define it twice, and the objects it clashes with.
netbox-trueup-ai compare -o json | jq -c '.objects[] | select(.outcome == "conflict")
  | {identity, collisions, duplicated_in, clashes}'
```

## Try it against a disposable NetBox

To see a comparison without a NetBox of your own, start the disposable one this repository uses for its tests (Docker needed, [internal/netboxtest](../internal/netboxtest/README.md)):

```sh
make netbox-up
export TRUEUP_NETBOX_URL=http://127.0.0.1:8000
export TRUEUP_NETBOX_TOKEN_FILE=$PWD/.cache/netbox/token
```

Never point these commands, or the token in that file, at another NetBox.

Then import a few of the library's device types through NetBox's own YAML import, in its web UI, signed in as `admin` with the password `insecure-test-only`:

1. Under Devices, Manufacturers, Import, paste the manufacturers, with the slugs `list` shows:

   ```yaml
   - name: Dell
     slug: dell
   ```

2. Under Devices, Device Types, Import, paste a file from the library, such as `device-types/Dell/PowerEdge-R640.yaml`, as it is.

Run `netbox-trueup-ai compare`: the manufacturer and the device type you imported are compared, and in step, and the rest of the library is counted. `netbox-trueup-ai compare --manufacturer Dell` adds Dell's other device types, absent. Edit the device type in NetBox, such as its height, and compare again to see the difference. `make netbox-down` removes it all.
