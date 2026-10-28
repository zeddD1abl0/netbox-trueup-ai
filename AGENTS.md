# AGENTS.md

Instructions for AI coding agents working on NetBox TrueUp AI. Human contributors should start with [CONTRIBUTING.md](CONTRIBUTING.md). Everything here applies to humans too; this file is just more explicit.

## Project

NetBox TrueUp AI is a Go server that keeps [NetBox](https://github.com/netbox-community/netbox) in step with YAML definitions held in Git repositories and, later, other sources. See [README.md](README.md) for the planned feature set.

**Status:** early development. The command tree, configuration and logging exist, integration tests can start a disposable NetBox, a read-only NetBox client backs `netbox check`, `source fetch` fetches Git sources into a cache and records each fetch in a database, on SQLite or PostgreSQL, and `migrate` creates and migrates that database. Nothing parses source files or writes to NetBox yet. The planned design is in [docs/architecture.md](docs/architecture.md) and the order of work in [docs/roadmap.md](docs/roadmap.md). Before assuming a component exists, check the code, [docs/adr/](docs/adr/) and [docs/tooling.md](docs/tooling.md).

**NetBox target:** 4.7 ([ADR 0003](docs/adr/0003-initial-netbox-target.md)). Your training data about NetBox may be out of date; verify against the reference source (below) or current documentation.

## Commands

| Task | Command |
| --- | --- |
| Install the pinned toolchain | `make tools` (needs [mise](https://mise.jdx.dev)) |
| Build | `make build` |
| Test (with race detector) | `make test` |
| Integration tests, against a disposable NetBox and PostgreSQL (needs Docker) | `make test-integration` |
| Start or remove a disposable NetBox for manual testing | `make netbox-up`, `make netbox-down` |
| Regenerate the code generated from SQL, and check it | `make generate`, `make generate-check` |
| Format | `make fmt` |
| Lint Go | `make lint` |
| Lint workflows and Markdown, scan for secrets | `make lint-repo` |
| Vulnerability check | `make vuln` |
| Everything CI runs, apart from the integration tests | `make check` |
| Fetch NetBox source for reference | `make reference` |

Run `make check` before proposing a pull request, and `make test-integration` if Docker is available.

## Layout

| Path | Contents |
| --- | --- |
| `cmd/netbox-trueup-ai/` | Entry point and cobra commands |
| `internal/` | Application packages |
| `internal/netbox/` | The NetBox REST client, read-only for now ([ADR 0019](docs/adr/0019-own-netbox-rest-client.md)) |
| `internal/netboxtest/` | The disposable NetBox: `compose.yaml`, and the package that starts it for integration tests and seeds it. See its README |
| `internal/source/` | Sources: snapshots, file listings, path checks and limits for every kind ([ADR 0009](docs/adr/0009-source-kinds-and-format-adapters.md)); `source/git/` is the Git kind ([ADR 0020](docs/adr/0020-git-sources-with-go-git.md)) |
| `internal/glob/` | Include and exclude patterns, with `**` |
| `internal/localdocker/` | Checks that tests' containers run on this machine's Docker and publish ports on 127.0.0.1 only, for `netboxtest` and `pgtest` |
| `internal/filelock/` | Operating system file locks, shared and exclusive |
| `internal/store/` | The storage interface; `store/sqlite/` and `store/postgres/` are the engines, each with its migrations, queries and sqlc's generated package, `store/engine/` chooses the engine, `store/storetest/` holds the tests every engine runs, and `store/postgres/pgtest/` starts PostgreSQL for them ([ADR 0011](docs/adr/0011-store-state-with-sqlc-and-goose.md), [ADR 0021](docs/adr/0021-forward-only-migrations-when-opening.md), [ADR 0022](docs/adr/0022-postgresql-schema-and-advisory-locks.md)) |
| `docs/` | Documentation. `docs/architecture.md` describes the design, `docs/roadmap.md` the planned steps, `docs/adr/` holds decision records, and `docs/tooling.md` lists every tool and why it was chosen |
| `.github/` | CI workflows, Dependabot, issue and PR templates |
| `.claude/` | Claude Code project settings and skills |

## Rules

### Always

- Work on a branch and open a pull request. `main` only changes through merged PRs.
- Write commit messages and PR titles as [Conventional Commits](https://www.conventionalcommits.org): `type(scope): summary`, imperative mood, at most 72 characters. Types: `feat`, `fix`, `docs`, `refactor`, `perf`, `test`, `build`, `ci`, `chore`, `revert`.
- Sign every commit. If signing fails, stop and tell the human; don't work around it.
- Keep each PR to one logical change, with tests for any behaviour change.
- Update documentation in the same PR: `docs/tooling.md` for any new or changed tool or dependency, an ADR for any significant decision ([what counts](docs/adr/README.md)), and the README of any directory whose usage changes.
- Keep the attribution your tool adds to commits and pull requests, such as a `Co-Authored-By` trailer.

### Never

- Never commit secrets, tokens, or real hostnames, addresses or data from anyone's network. Use documentation values: `example.com`, `192.0.2.0/24`, `2001:db8::/32`.
- Never write to a NetBox instance other than a disposable local one (`internal/netboxtest`), and never run a sync against shared or production NetBox.
- Never push to `main`, approve a pull request, or merge one. A human maintainer does those.
- Never bypass checks: no `--no-verify`, no `--no-gpg-sign`, no `-c commit.gpgsign=false`.
- Never weaken a CI check, linter rule or security setting to make a change pass. Explain the problem to a human instead.
- Never add a dependency without a reason in the PR description. Prefer the standard library.
- Never edit generated files by hand. Change the source and regenerate: for the database, change the migrations and queries and run `make generate`.

## Go conventions

- Format with `make fmt` (gofumpt and goimports).
- Wrap errors with context and `%w`: `fmt.Errorf("load source %q: %w", name, err)`. Either handle an error or return it, not both. No panics outside `main`.
- Functions that do I/O take a `context.Context` as their first parameter.
- Log with `log/slog` using structured key-value pairs. Never log secrets or tokens.
- Write table-driven tests using the standard library, plus `go-cmp` for comparing values. Call `t.Parallel()` where it's safe.
- Put packages under `internal/` unless they are deliberately public API.
- Commands follow [ADR 0018](docs/adr/0018-cli-conventions.md): results on stdout, everything else on stderr, `--output json` for scripts, and its exit codes.
- A new setting goes in the table in `internal/config/schema.go` and in [docs/configuration.md](docs/configuration.md). A secret uses the `config.Secret` type, so it can't be printed or logged.

## Matching NetBox behaviour

The comparison engine must treat fields the way NetBox does. Before writing comparison rules for a model or field, read NetBox's own code at the target version: `make reference` puts it in `.cache/reference/`. Cite the NetBox source you followed in the PR description.

## Security

Treat everything from source repositories, YAML files, the web UI and NetBox responses as untrusted input. Vulnerabilities are reported privately; see [SECURITY.md](SECURITY.md).
