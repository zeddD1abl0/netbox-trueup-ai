# Contributing

Thanks for your interest in NetBox TrueUp AI. This project is AI-first (see the [README](README.md#why-ai-is-in-the-name)). Contributions written by hand, with AI help, or by an AI agent you supervise are all welcome, and all follow the same rules.

## Before you start

- For anything bigger than a small fix, open an issue first so we can agree on the approach before you invest time.
- Report security problems privately, as described in [SECURITY.md](SECURITY.md).
- Follow the [Code of Conduct](CODE_OF_CONDUCT.md).

## Setting up

Full instructions are in [docs/tooling.md](docs/tooling.md#10-setting-up-a-development-environment). In short:

```sh
mise install   # pinned toolchain from mise.toml
make hooks     # Git hooks: format, lint, secret scan, commit message check
make check     # everything CI runs
```

## Making a change

1. Fork the repository and create a branch from `main` named `type/short-description`, for example `feat/device-type-sync`.
2. Make the change, with tests. Update documentation in the same pull request:
   - the README of any directory whose usage changes;
   - [docs/tooling.md](docs/tooling.md) if you add, remove or replace a tool or dependency;
   - an [ADR](docs/adr/README.md) if you make a significant decision.
3. Run `make check`. With Docker, run `make test-integration` too: it runs the integration tests against a disposable NetBox, and the storage tests against PostgreSQL, as CI does. `make netbox-up` starts the same NetBox for trying things by hand (see [internal/netboxtest](internal/netboxtest/README.md)).
4. Commit using [signed](#signing-commits) [Conventional Commits](#commit-messages).
5. Open a pull request and fill in the template.

## Commit messages

We use [Conventional Commits](https://www.conventionalcommits.org):

```text
type(optional-scope): short imperative summary

Optional body explaining what changed and why.
```

| Type | Use for |
| --- | --- |
| `feat` | A new feature |
| `fix` | A bug fix |
| `docs` | Documentation only |
| `refactor` | Code changes that neither fix a bug nor add a feature |
| `perf` | Performance improvements |
| `test` | Adding or fixing tests |
| `build` | Build system or dependencies |
| `ci` | CI configuration |
| `chore` | Other maintenance |
| `revert` | Reverting an earlier change |

Add `!` after the type for breaking changes, for example `feat(config)!: rename sync.interval`.

Pull requests are squash-merged, so **the PR title becomes the commit message on `main`**. A CI check enforces the format on PR titles, and it drives the changelog.

## Signing commits

Every commit in a pull request must carry a signature that GitHub can verify. A CI check rejects pull requests containing unsigned commits.

You can sign with an SSH key or a GPG key. With SSH:

```sh
git config --global gpg.format ssh
git config --global user.signingkey ~/.ssh/id_ed25519.pub
git config --global commit.gpgsign true
```

Then add the same public key to your GitHub account as a **Signing key** (Settings → SSH and GPG keys). Your commit email must be a verified email on that account. For GPG, see GitHub's guide to [signing commits](https://docs.github.com/en/authentication/managing-commit-signature-verification/signing-commits).

To sign commits already on your branch:

```sh
git rebase --exec 'git commit --amend --no-edit -S' origin/main
git push --force-with-lease
```

## AI-assisted contributions

- You are the author. Understand every line you submit, and be ready to explain it and respond to review.
- You don't need to describe how you used AI. If your tool adds attribution, such as a `Co-Authored-By` trailer, leave it in.
- Point your agent at [AGENTS.md](AGENTS.md). Claude Code reads [CLAUDE.md](CLAUDE.md), which includes it, and the project's plugins and settings live in `.claude/`.
- Every pull request needs a human who has reviewed the change and is accountable for it. Unreviewed agent output will be closed.

## Review and merge

A maintainer reviews each pull request. Once CI passes and review comments are resolved, the maintainer squash-merges it.

## License

By contributing, you agree that your contributions are licensed under the [Apache License 2.0](LICENSE), as described in section 5 of the license.
