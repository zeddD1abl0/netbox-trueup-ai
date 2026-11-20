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

- **Which updates.** The workflow `.github/workflows/dependabot-auto-merge.yml` runs on Dependabot's pull requests. It reads each update with [dependabot/fetch-metadata](https://github.com/dependabot/fetch-metadata), and runs `gh pr merge --auto --squash` when all of these hold:
  - it updates Go modules, or images in the tests' compose files;
  - the largest update in the pull request is a patch or minor release, which includes security updates, since they skip Dependabot's cooldown;
  - it touches neither a go-git module, since go-git and go-billy are betas taken through reviewed updates ([ADR 0020](0020-git-sources-with-go-git.md)), nor the NetBox image, since NetBox's version is the target ([ADR 0003](0003-initial-netbox-target.md)).

  Major releases, GitHub Actions, go-git and NetBox wait for the maintainer, as other pull requests do.
- **Merging.** GitHub merges when the ruleset's required checks pass, and only then. The integration tests become a required check, because they are the only tests that start PostgreSQL, Valkey and NetBox, and auto-merge doesn't wait for the others. The repository allows auto-merge.
- **Accountability.** This is an exception to [ADR 0004](0004-contribution-workflow.md)'s "maintainer merges", and to [ADR 0005](0005-ai-first-development.md)'s "every pull request has a human who has reviewed it", for these updates only. The maintainer reviews the rule, in this workflow and in `.github/dependabot.yml`, rather than each update, and changing the rule takes a reviewed pull request. Agents still never approve or merge pull requests.

### Consequences

- Good, because routine updates land once their checks pass, instead of days later.
- Good, because they pass the same checks as every other change, and GitHub signs the squash commits it makes.
- Bad, because a compromised release that outlasts the 7-day cooldown and passes CI merges without anyone seeing it. The cooldown gives others a week to find a bad release, and only a release the maintainer makes ships it to users.
- Bad, because every pull request now waits for the integration tests, 16 to 24 minutes in CI, before it can merge.
- Bad, because GitHub starts no workflows for what a workflow's own token does, so CI isn't expected to run on `main` after these merges. Each update was tested on its pull request. The ruleset doesn't require branches to be up to date, so two updates merged close together are first tested together by the next pull request's CI. A GitHub App's token would start them, but needs a stored secret.
- Neutral, because one major update, go-git module or NetBox image in a group holds back the whole group, as all of it waited before.
- Neutral, because turning off auto-merge on one pull request lasts until Dependabot next pushes to it; closing the pull request holds it back for longer.

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
- fetch-metadata v3.1.0: `update-type` is the largest update in the pull request, and is worked out from the versions when Dependabot's commit doesn't give it, as for images (`src/dependabot/update_metadata.ts`, `src/dependabot/output.ts`). `package-ecosystem` comes from the branch name (`go_modules`, `docker_compose`, `github_actions`). It fails unless the pull request's first commit is Dependabot's and has a signature GitHub verified (`src/dependabot/verified_commits.ts`).
