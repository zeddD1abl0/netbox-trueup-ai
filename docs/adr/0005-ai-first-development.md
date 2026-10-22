---
status: accepted
date: 2026-10-22
decision-makers: "@zeddD1abl0"
---

# AI-first development

## Context and problem

Most of this project's code is written by AI coding agents, directed by a human maintainer. Opinions on AI-written software vary widely, so users deserve to know this up front. The project also needs rules that keep agent-written code to the same standard as any other code, and that let any contributor reproduce the setup with their own AI account.

## Decision drivers

- Transparency with users and contributors.
- Human accountability for everything that ships.
- The same quality bar regardless of who, or what, wrote the code.
- A reproducible environment for AI-assisted development.

## Decision

- **Say it in the name.** The project is called NetBox TrueUp AI (`netbox-trueup-ai`), and the README explains what AI-first means here.
- **Public agent instructions.** [AGENTS.md](../../AGENTS.md) is the canonical, tool-neutral guide for agents. [CLAUDE.md](../../CLAUDE.md) imports it and adds Claude Code specifics. The shared Claude Code configuration (plugins, permissions) is checked in under `.claude/`, and [docs/tooling.md](../tooling.md) explains every tool and skill and how to set them up.
- **Humans stay accountable.** Agents never push to `main`, approve or merge pull requests, or write to any NetBox instance other than a disposable local one. Every pull request has a human who has reviewed it.
- **Same bar for everyone.** CI checks, linters, security scanners and tests apply to every change, whoever wrote it.
- **Attribution, not itemized disclosure.** AI tools credit themselves in commit trailers and pull request descriptions. Pull requests don't itemize how AI was used: the project name already says it, and the answer wouldn't change how a change is reviewed.
- **No automated AI review on pull requests for now.** On a public repository it would need an API key secret, and it would process untrusted pull request content, which risks prompt injection. Revisit with a design that needs no secrets.

### Consequences

- Good, because users can judge the project knowing how it is built.
- Good, because agent behaviour is governed by reviewable files, not private prompts.
- Bad, because some people will avoid the project because of the name. We accept that in exchange for transparency.
- Agent instruction files must be kept accurate as the project changes; the `claude-md-management` plugin helps with that.
