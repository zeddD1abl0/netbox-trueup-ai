---
status: accepted
date: 2026-10-23
decision-makers: "@zeddD1abl0"
---

# CLI conventions: command names, exit codes and machine-readable output

## Context and problem

The command line is TrueUp's first interface, and it stays a public one: people type it, and scripts, CI jobs and systemd units run it. All of them depend on command names, exit codes and output staying predictable. Every command added from milestone 1 on needs the same answers: what it is called, what its exit code means, where its output goes, and how a script reads it.

## Decision drivers

- Predictable for people and for scripts.
- Machine-readable output that scripts can rely on from one release to the next.
- Exit codes that tell a mistake on the command line or in the configuration from a failed run.
- Secrets never on a command line.
- Close to what cobra, kubectl and the Go toolchain already teach.

## Considered options

Command names:

1. Top-level verbs for the main workflow, and groups named for what they act on for everything else
2. Noun, then verb, everywhere (`definition list`, `object compare`)
3. Verb, then noun, everywhere (`get definitions`, `show config`)

Exit codes:

1. 0 to 3, with higher codes reserved for outcomes particular to a command
2. 0 and 1 only
3. The codes in sysexits.h (64 for usage, 78 for configuration)

Machine-readable output:

1. `--output text|json` on each command that prints a result
2. A `--json` switch
3. JSON whenever stdout isn't a terminal

## Decision

Chosen options: "top-level verbs and groups", "0 to 3, with higher codes reserved" and "`--output text|json`".

**Command names.**

- The everyday workflow is top-level verbs: `list`, `compare`, `import`, `plan`, `apply`, `history` and `audit`, and later `migrate` and `serve`.
- Everything else is a group named for what it acts on, then a verb: `config show`, `source fetch`, `netbox check`, `permissions export`.
- Names are lower case, one word where possible and hyphenated otherwise. `version`, `help` and `completion` complete the tree.
- A group run on its own prints its help. An unknown command is a usage error, with a suggestion when one is close.

**Flags.**

- Long flags are kebab-case. The only short flags are `-h` and `-o`.
- A flag that sets a setting is named after its key, with dots and underscores as hyphens: `log.level` is `--log-level`, and `netbox.token_file` is `--netbox-token-file` ([ADR 0012](0012-layered-configuration.md)).
- A secret is never a flag's value. Other users of a host can read command lines, and shells keep them in their history. A secret's flag takes the file of its `_file` companion instead.
- Options about one run of a command rather than how TrueUp behaves, such as `--config` and `--output`, are flags only, not settings.

**Streams.** stdout carries only the command's result. Logs, warnings, errors and progress go to stderr. With `--output json`, stdout is therefore always a single JSON document, or empty if the command failed.

**`--output`.**

- Every command that prints a result takes `--output` (`-o`): `text`, the default, or `json`.
- JSON output is one indented document with snake_case field names. It is part of the public interface: fields can be added, but renaming or removing a field, or changing its type, is a breaking change, marked with `!` in the pull request title and called out in the changelog.
- Text output is for people. Its layout can change in any release, so scripts should use JSON.

**Exit codes.**

| Code | Meaning |
| --- | --- |
| 0 | Success. |
| 1 | The command failed, for example because NetBox couldn't be reached. |
| 2 | Usage error: an unknown command or flag, or bad arguments. Go's `flag` package and most shells use 2 for the same. |
| 3 | Configuration error: settings that are invalid or can't be read, including a config file or secret file that is missing. |
| 4 and up | Reserved for outcomes particular to one command, such as `compare` finding differences when asked to signal them. Each is documented in its command's help. |

A systemd unit can set `RestartPreventExitStatus=2 3`, so that a service whose configuration is broken stops instead of restarting in a loop.

**Errors.** A failed command writes `Error:` and the message to stderr, followed for a usage error by a pointer to `--help`. Once the configuration has loaded with `log.format: json`, the error is written as a JSON log record instead, so that log pipelines can parse it.

### Consequences

- Good, because scripts can rely on JSON output and exit codes, and a service manager can tell a broken configuration from a failure.
- Good, because secrets stay off command lines.
- Bad, because JSON fields are a compatibility commitment from the first release.
- Bad, because a secret can't be given as a flag, even for a quick test. An environment variable or a file does the job.

## Pros and cons of the options

### Noun, then verb, everywhere

- Good, because every command has the same shape.
- Bad, because the commands people run most get longer: `object compare` instead of `compare`.

### Verb, then noun, everywhere

- Good, because commands read like sentences, as kubectl's `get pods` does.
- Bad, because the verbs differ from noun to noun: a plan is applied, a source fetched and a permission set exported. A fixed set of verbs wouldn't fit, and an open set is hard to discover.

### 0 and 1 only

- Good, because it is the simplest.
- Bad, because neither scripts nor service managers can tell a mistake in the command or the configuration from a failed run.

### The codes in sysexits.h

- Good, because their meanings are written down in one place.
- Bad, because few current command-line tools use them, the numbers are unfamiliar, and Go's own tools use 2 for usage errors.

### A `--json` switch

- Good, because it is short.
- Bad, because another format, such as YAML, would need a second switch.

### JSON whenever stdout isn't a terminal

- Good, because a pipe gets JSON without asking.
- Bad, because the output depends on how a command is run, which surprises anyone piping text to `grep`, and makes scripts behave differently from a terminal.
