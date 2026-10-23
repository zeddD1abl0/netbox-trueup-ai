# NetBox TrueUp AI

Keep NetBox true to the YAML you keep in Git.

> **Status: early development.** Nothing here is usable yet. To follow progress, watch the repository or read the [decision records](docs/adr/).

NetBox TrueUp AI is a server that synchronizes infrastructure definitions written in YAML into [NetBox](https://github.com/netbox-community/netbox). Device types from the [NetBox device-type library](https://github.com/netbox-community/devicetype-library) are one example of such a source.

## Planned features

- Read definitions from Git repositories first, and later from HTTP and local file sources.
- Sync manufacturers and device types first, then module types, module type profiles, rack types and more.
- Choose what syncs, and edit the YAML before it syncs, from a web interface.
- Compare definitions the way NetBox itself does, and show the differences between versions of an object side by side: every source that defines it, NetBox's copy, and your live edits.
- Choose which source each object follows, and take individual fields from another source, from NetBox, or from your edits.
- Inject values and override fields at import time. Overrides are remembered, so the next sync doesn't undo them.
- Record which source and file produced each NetBox object, and when it last synced.
- Record who synced what, and when, and stream the audit log to other systems.
- Sign in with OpenID Connect or SAML single sign-on, with users and groups provisioned through SCIM.
- Fit corporate change management when you need it: approvals by the right people, a change reference on every sync, and changes written through NetBox branches. A single user can leave all of it off.
- React to webhooks from Git hosts and NetBox, and send webhooks when something syncs.
- Expose metrics for Prometheus, and metrics and traces for OpenTelemetry.
- Generate the NetBox permission set that its API token needs, ready to import.
- Run as a single binary under systemd or as a Windows service, or in Docker or Kubernetes.
- Run on a single node with SQLite, or as a highly available cluster with PostgreSQL.
- Configure it with command-line flags, environment variables, a config file or the web interface.

The first releases target **NetBox 4.7**.

## Why "AI" is in the name

This project is AI-first. Most of its code is written by AI coding agents (currently Claude Code), directed by a human maintainer who reviews every change. The name says so up front rather than leaving it to the fine print.

In practice:

- Every change reaches `main` through a pull request that a human reviews and merges.
- The same CI checks, linters, security scanners and tests apply whether a person or an agent wrote the code.
- Commits and pull requests credit the AI tools that helped write them.
- The instructions agents follow are public: [AGENTS.md](AGENTS.md), [CLAUDE.md](CLAUDE.md) and [docs/tooling.md](docs/tooling.md).

## Documentation

- [docs/](docs/): documentation index
- [docs/architecture.md](docs/architecture.md): how it is designed
- [docs/roadmap.md](docs/roadmap.md): what gets built next
- [docs/tooling.md](docs/tooling.md): every tool and library we use, why, and how to set up a development environment
- [docs/adr/](docs/adr/): architecture decision records

## Contributing

Contributions are welcome, whether written by hand, with AI help, or by an agent you supervise. Start with [CONTRIBUTING.md](CONTRIBUTING.md). AI agents should read [AGENTS.md](AGENTS.md).

## Security

Please report vulnerabilities privately as described in [SECURITY.md](SECURITY.md), not in public issues.

## License

[Apache License 2.0](LICENSE).

NetBox TrueUp AI is an independent project. It is not affiliated with or endorsed by NetBox Labs.
