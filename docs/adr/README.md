# Architecture decision records

An architecture decision record (ADR) captures one significant decision: the problem, the options we considered, what we chose and what follows from it. They explain *why* the project is the way it is, to future contributors and to AI agents.

## Index

| ADR | Title | Status |
| --- | --- | --- |
| [0001](0001-record-architecture-decisions.md) | Record architecture decisions | Accepted |
| [0002](0002-license.md) | License under Apache 2.0 | Accepted |
| [0003](0003-initial-netbox-target.md) | Target NetBox 4.7 first | Accepted |
| [0004](0004-contribution-workflow.md) | Contribution workflow | Accepted |
| [0005](0005-ai-first-development.md) | AI-first development | Accepted |
| [0006](0006-manage-only-linked-objects.md) | Manage only objects a person has imported | Accepted |
| [0007](0007-provenance-and-changelog-messages.md) | Keep provenance in TrueUp, and mark NetBox writes with changelog messages only | Accepted |
| [0008](0008-describe-object-types-with-specs.md) | Describe NetBox object types with declarative specs | Accepted |
| [0009](0009-source-kinds-and-format-adapters.md) | Read sources through source kinds and format adapters | Accepted |
| [0010](0010-write-through-reviewed-plans.md) | Write to NetBox only through reviewed plans | Accepted |
| [0011](0011-store-state-with-sqlc-and-goose.md) | Store state in SQLite or PostgreSQL through sqlc and goose | Accepted |
| [0012](0012-layered-configuration.md) | Layer configuration from flags, environment, file and database | Accepted |
| [0013](0013-compose-objects-across-sources.md) | Compare objects across sources side by side, and compose them field by field | Accepted |
| [0014](0014-audit-every-action.md) | Record every action in an append-only audit log, and stream it out | Accepted |
| [0015](0015-sign-in-and-provisioning.md) | Sign in with OpenID Connect, SAML or local accounts, provision with SCIM, and never trust proxy headers | Accepted |
| [0016](0016-permissions-and-approval-policies.md) | Scoped permissions, and approval policies and change references that can be turned on | Accepted |
| [0017](0017-netbox-branches.md) | Write plans through NetBox branches where teams want them | Accepted |

## When to write one

Write an ADR in the same pull request when a change:

- adds a runtime dependency or replaces a tool;
- changes how data is stored, or the database schema strategy;
- changes a public interface: CLI, configuration, HTTP API or file formats;
- changes the security posture: authentication, permissions, secrets or supply chain;
- adds or drops a supported platform, database or NetBox version.

When in doubt, write one. A short ADR costs little; a forgotten reason costs a lot.

## How to write one

1. Copy [template.md](template.md) to `NNNN-short-title.md`, using the next free number.
2. Fill it in. Keep it short; link to issues and PRs for detail.
3. Add it to the index above.

ADRs are not edited after they are accepted, apart from fixing typos and broken links. To change a decision, write a new ADR that supersedes the old one, and set the old one's status to `superseded by ADR-NNNN`.
