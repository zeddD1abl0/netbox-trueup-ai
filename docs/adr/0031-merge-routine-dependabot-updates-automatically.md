---
status: accepted
date: 2026-11-20
decision-makers: "@zeddD1abl0"
---

# Merge Dependabot's patch and minor updates of Go modules and test databases automatically, once every required check passes

## Context and problem

Dependabot opens two to four pull requests a week: one for Go modules, one for GitHub Actions, and one for each set of test database images. Those merged so far waited one to five days for the maintainer (#5 to #22). [ADR 0004](0004-contribution-workflow.md) has the maintainer merge every pull request, and [ADR 0005](0005-ai-first-development.md) says every pull request has a human who has reviewed it.

Should routine updates wait for the maintainer, or merge themselves once CI has checked them?

## Decision drivers

- A person decides anything that changes what the project targets or trusts: major releases, prereleases, NetBox's version, and the actions CI runs.
- Nothing merges that CI hasn't exercised.
- No stored secrets and no third-party apps with write access ([ADR 0005](0005-ai-first-development.md), [tooling.md](../tooling.md#9-considered-and-not-chosen)).
- History on `main` stays signed and squash-merged ([ADR 0004](0004-contribution-workflow.md)).

## Considered options

1. The maintainer merges every Dependabot pull request, as now
2. A workflow turns on auto-merge for some kinds of update
3. A workflow turns on auto-merge for every update
4. Renovate's automerge

## Decision

Chosen option: "a workflow turns on auto-merge for some kinds of update", because it saves the maintainer's time on the updates CI checks fully, and leaves the rest as they are.

- **Which updates.** The workflow `.github/workflows/dependabot-auto-merge.yml` runs whenever a pull request Dependabot opened is opened, pushed to or reopened. It reads the update with [dependabot/fetch-metadata](https://github.com/dependabot/fetch-metadata), and turns on auto-merge, to squash, when:
  - every commit in the pull request is Dependabot's, with a signature GitHub verified, so nothing anyone else pushes merges without review;
  - and every dependency it updates is a Go module or an image in the tests' compose files, has a patch or minor update, which includes security updates, since they skip Dependabot's cooldown, and is neither go-git nor go-billy, which are betas taken through reviewed updates ([ADR 0020](0020-git-sources-with-go-git.md)), nor NetBox, whose version is the target ([ADR 0003](0003-initial-netbox-target.md)).

  Otherwise it turns auto-merge off, if it was on, and the pull request waits for the maintainer, as other pull requests do: major releases, GitHub Actions, go-git, NetBox, updates whose size fetch-metadata can't tell, and any Dependabot pull request someone else has pushed to. go-git and go-billy have a Dependabot group of their own, so they don't hold back the other Go modules.
- **Merging.** GitHub merges when the ruleset's required checks pass, and only then. The integration tests become a required check, because they are the only tests that start PostgreSQL, Valkey and NetBox, and auto-merge doesn't wait for the others. The repository allows auto-merge.
- **Accountability.** This is an exception to [ADR 0004](0004-contribution-workflow.md)'s "maintainer merges", and to [ADR 0005](0005-ai-first-development.md)'s "every pull request has a human who has reviewed it", for these updates only. The maintainer reviews the rule, in this workflow and in `.github/dependabot.yml`, rather than each update, and changing the rule takes a reviewed pull request. Agents still never approve or merge pull requests, and a commit an agent pushes to a Dependabot pull request turns its auto-merge off.

### Consequences

- Good, because routine updates land once their checks pass, instead of days later.
- Good, because they pass the same checks as every other change, and GitHub signs the squash commits it makes.
- Bad, because a compromised release that outlasts the 7-day cooldown and passes CI merges without anyone seeing it. The cooldown gives others a week to find a bad release, and only a release the maintainer makes ships it to users.
- Bad, because every pull request now waits for the integration tests, 16 to 24 minutes in CI, before it can merge.
- Bad, because GitHub starts no workflows for what a workflow's own token does, so CI isn't expected to run on `main` after these merges. Each update was tested on its pull request. The ruleset doesn't require branches to be up to date, so two updates merged close together are first tested together by the next pull request's CI. A GitHub App's token would start CI, but needs a stored secret. Requiring branches to be up to date would stall auto-merge: Dependabot doesn't update a branch that is only behind, and updating it from GitHub adds a commit that isn't Dependabot's. A merge queue can't be fed with a workflow's own token.
- Bad, because, in Go, a major release is a new module path, which Dependabot doesn't propose, and a minor release of a v0 module may change its API. So in practice every Go update but go-git's merges once CI passes. The compiler and the tests catch API changes, but not changes of behaviour the tests don't cover. Treating v0 minor releases as major would hold back the `golang.org/x` modules, which are v0 and in most weeks' updates.
- Neutral, because a group with one update that doesn't qualify waits as a whole, as all of it waited before.
- Neutral, because turning auto-merge off by hand lasts until Dependabot next pushes to the pull request or it is reopened. Closing it holds it back, and pushing a commit to it hands it to the maintainer.

## Pros and cons of the options

### The maintainer merges every Dependabot pull request

- Good, because a person sees every change before it lands.
- Bad, because, for routine updates, that person adds little to what the checks and the cooldown already do, and updates wait days.

### Auto-merge for every update

- Bad, because it would merge major releases, which change behaviour, and updates to the actions CI runs with tokens, without a person deciding.
- Bad, because it would merge go-git betas, which [ADR 0020](0020-git-sources-with-go-git.md) takes through reviewed updates.

### Renovate's automerge

- Good, because it is more configurable, and covers `mise.toml`.
- Bad, because it means installing a third-party app with write access, which [tooling.md](../tooling.md#9-considered-and-not-chosen) declined.

## More information

- GitHub: [automating Dependabot with GitHub Actions](https://docs.github.com/en/code-security/dependabot/working-with-dependabot/automating-dependabot-with-github-actions), and [GITHUB_TOKEN](https://docs.github.com/en/actions/concepts/security/github_token) on events its use doesn't trigger.
- fetch-metadata v3.1.0: `updated-dependencies-json` gives each dependency's update type, worked out from the versions when Dependabot's commit doesn't say, as for images, and empty when that fails; `update-type` is the largest of those it knows, so the workflow checks each dependency instead (`src/dependabot/update_metadata.ts`, `src/dependabot/output.ts`). `packageEcosystem` comes from the branch name (`go_modules`, `docker_compose`, `github_actions`). It fails unless the pull request's first commit is Dependabot's and has a signature GitHub verified, and looks at no other commit (`src/dependabot/verified_commits.ts`), so the workflow checks them all.
