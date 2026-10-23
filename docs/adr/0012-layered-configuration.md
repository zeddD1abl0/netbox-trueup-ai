---
status: accepted
date: 2026-10-23
decision-makers: "@zeddD1abl0"
---

# Layer configuration from flags, environment, file and database

## Context and problem

The README promises configuration through command-line flags, environment variables, a config file and the web interface. Some settings are needed before the database opens, and others should be changeable at runtime from the UI. Teams that keep configuration in Git need their file to stay authoritative. Wherever a value comes from, which one wins must be predictable.

## Decision drivers

- One predictable order of precedence.
- Works in containers (environment variables, secret files) and with configuration kept in Git.
- Edits in the UI never fight the config file.
- Secrets never leak.

## Considered options

1. Declared settings layered by koanf, plus runtime settings in the database that a declaration locks
2. The database as the only store after first start
3. The config file only, with no editing in the UI

## Decision

Chosen option: "Declared settings layered by koanf, plus runtime settings in the database that a declaration locks".

- **Every setting can be declared.** Precedence, highest first:
  1. command-line flags;
  2. environment variables with the `TRUEUP_` prefix;
  3. the YAML config file;
  4. defaults.

  Runtime settings stored in the database (below) sit between the file and the defaults, so the full order is flags, environment, file, database, defaults.
- **Variable names.** Keys are snake_case. A variable name is the prefix plus the key path in upper case, with dots between levels becoming underscores: `database.url` becomes `TRUEUP_DATABASE_URL`. That mapping could give two settings the same name, so a test checks that every setting maps to a distinct variable.
- **Secrets from files.** Each secret setting has a companion setting with `_file` appended to its key, naming a file to read the secret from: `netbox.token_file` in the config file, or `TRUEUP_NETBOX_TOKEN_FILE` in the environment. Only secret settings have these companions. Setting both a secret and its file in the same layer is an error; across layers, the higher layer wins, whichever form it uses.
- **Process settings**, such as the listen address, database, data directory and logging, can only be declared. Changing them needs a restart.
- **Runtime settings**, such as sources, the NetBox connection, schedules and sync policies, can also be edited in the UI or CLI, and are stored in the database. When a runtime setting is declared, the declared value wins, and the UI shows it read-only along with where it came from.
- **Secret handling.** Secrets are never logged or displayed, and `config show` redacts them. Until an ADR covers encrypting secrets entered in the UI, secrets come only from declarations.
- cobra parses flags, and koanf merges the layers ([tooling.md §3.6](../tooling.md#36-configuration-cli-and-service-integration)).

### Consequences

- Good, because containers and Git-managed deployments work without touching the UI, and UI users still get editable settings.
- Bad, because a runtime setting can come from two places. The UI must always show which.
- Bad, because list-valued settings such as sources are awkward to set through environment variables. The config file suits them better.

## Pros and cons of the options

### The database as the only store

- Good, because there is one place to look at runtime.
- Bad, because configuration kept in Git stops being authoritative after the first start.

### The config file only

- Good, because it is the simplest.
- Bad, because it drops editing in the UI, which the README promises.
