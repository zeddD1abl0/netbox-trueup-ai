---
status: accepted
date: 2026-10-22
decision-makers: "@zeddD1abl0"
---

# Record architecture decisions

## Context and problem

The project is public from its first commit, and most of its code is written by AI agents working with a human maintainer. Neither a new contributor nor an agent starting a fresh session can see the conversations in which decisions were made. Without a written record, decisions get re-argued or quietly reversed.

## Decision drivers

- Reasons must survive beyond the conversation that produced them.
- Humans and agents should be able to read them quickly, in the repository.
- Recording a decision should be cheap enough that people actually do it.

## Considered options

1. ADRs in the repository, in MADR format
2. A wiki or external documents
3. Rely on commit messages and PR descriptions

## Decision

Chosen option: "ADRs in the repository, in MADR format". ADRs live in `docs/adr/`, numbered, using a trimmed [MADR 4.0](https://adr.github.io/madr/) template. [docs/adr/README.md](README.md) defines when one is required. Accepted ADRs are superseded, not edited. Alongside them, [docs/tooling.md](../tooling.md) is the living inventory of tools, kept current in every PR that changes one.

### Consequences

- Good, because decisions are versioned and reviewed with the code that implements them.
- Good, because agents can be told to read and write them (see [AGENTS.md](../../AGENTS.md)).
- Bad, because each significant PR carries a little extra writing.

## Pros and cons of the options

### Wiki or external documents

- Bad, because they drift from the code and aren't reviewed with it.
- Bad, because agents working in the repository don't see them.

### Commit messages and PR descriptions

- Good, because they cost nothing extra.
- Bad, because they're hard to find later, and don't capture the options that were rejected.
