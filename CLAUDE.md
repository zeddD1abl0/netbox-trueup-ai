# CLAUDE.md

@AGENTS.md

## Claude Code specifics

The rules above apply in full. This section only adds Claude Code tooling; why each tool was chosen is in [docs/tooling.md](docs/tooling.md).

- **Plugins** are listed in `.claude/settings.json`. Accept the install prompt when you first trust the folder.
- **NetBox work:** load the relevant `netbox-labs` skills before designing NetBox-facing code: `netbox-api-integration` for the REST client, `netbox-data-modeling` for dependency order and natural keys, `netbox-administration` for permissions and tokens.
- **Go work:** the `golang-*` skills listed in docs/tooling.md section 1.3 apply if you have them installed. `gopls-lsp` gives you definitions, references and diagnostics.
- **Library documentation:** use `context7` for third-party APIs instead of relying on memory.
- **Before opening a PR:** run `/code-review`. Also run `/security-review` for anything touching authentication, secrets, network, file or database access. Run the `pr-review-toolkit` silent-failure-hunter on changes to sync, comparison or error-handling code.
- **Web UI changes:** check them in a real browser with the `playwright` plugin, and take design guidance from `frontend-design`.
