---
status: accepted
date: 2026-10-22
decision-makers: "@zeddD1abl0"
---

# Contribution workflow

## Context and problem

The project is public, AI-first, and has one maintainer to start with. Changes need to be reviewable, traceable to an accountable person, and recorded in a history clean enough to generate release notes from.

## Decision drivers

- Every change on `main` is reviewed by a human before it lands.
- Authorship can be verified.
- History and changelog are readable without extra effort.
- One maintainer must be able to run it.

## Decision

- **Pull requests only.** `main` is protected by a repository ruleset: changes arrive through pull requests with passing status checks, and force-pushes and deletion are blocked. The one exception was the initial commit, which created `main` before the ruleset could exist.
- **Maintainer merges.** No approving-review count is required, because GitHub doesn't let authors approve their own pull requests, and the maintainer's agent opens pull requests under the maintainer's account. Review happens before the maintainer merges.
- **Squash merge only,** with the pull request title as the commit message. This keeps `main` linear: one commit per pull request.
- **Conventional Commits** for pull request titles, enforced by CI, and for commit messages, checked by a local Git hook. They drive versioning and the changelog.
- **Signed commits.** Every commit must have a signature GitHub verifies: the ruleset enforces this on `main`, and a CI check enforces it on every commit in a pull request. GPG and SSH keys are both accepted.
- **Branch names** follow `type/short-description`.

### Consequences

- Good, because history on `main` doubles as the changelog source.
- Good, because each commit is cryptographically tied to a GitHub account.
- Bad, because contributors must set up commit signing before their first pull request. [CONTRIBUTING.md](../../CONTRIBUTING.md) explains how.
- Bad, because squash merging discards a branch's individual commits. The pull request keeps them for reference.

## Options considered and not chosen

- **Merge commits or rebase merging.** These keep branch commits on `main`, but make history noisier and changelog generation harder.
- **Developer Certificate of Origin (DCO) sign-off.** Apache 2.0 section 5 already covers how contributions are licensed, and signatures cover authorship. This can be revisited if a contributor's employer requires it.
- **Required approving reviews.** These would block every pull request the maintainer's agent opens, as explained above. Revisit when there is more than one maintainer.
