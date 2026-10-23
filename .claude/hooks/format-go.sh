#!/bin/sh
# Claude Code PostToolUse hook: formats a Go file after Claude edits or
# writes it. See docs/tooling.md section 1.5.
#
# It runs gofumpt only, not goimports. An edit can leave a file half done,
# with an import briefly unused or not yet added, and goimports would delete
# the import, or add whichever package of that name it finds in the module
# cache, which may be a dependency nobody chose. `make fmt`, the pre-commit
# hook and lint still run goimports on finished code.
#
# Claude Code passes the tool call as JSON on stdin. Only tool_input.file_path
# is needed, so sed picks it out and contributors don't need jq. Formatting
# is a convenience: failures are ignored, and lint catches anything left.

file=$(sed -nE 's/.*"file_path"[[:space:]]*:[[:space:]]*"(([^"\\]|\\.)*)".*/\1/p' | head -n 1 | sed 's/\\\(.\)/\1/g')

case $file in
  *.go) ;;
  *) exit 0 ;;
esac

# Only files in this repository.
root=${CLAUDE_PROJECT_DIR:-$(pwd)}
case $file in
  "$root"/*) ;;
  *) exit 0 ;;
esac

cd "$root" && .lefthook/with-tools golangci-lint fmt --no-config --enable gofumpt "$file" >/dev/null 2>&1
exit 0
