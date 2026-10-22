# Developer entry points. Run `make help` to list them; CI runs `make check`.
# Tool versions come from mise.toml (`make tools` installs them).

# Put the tools pinned in mise.toml first on PATH even when mise isn't activated
# in the calling shell, as with IDE tasks, scripts and AI agents. Without mise,
# tools come from PATH as usual.
ifneq ($(shell command -v mise 2>/dev/null),)
export PATH := $(shell mise bin-paths 2>/dev/null | tr '\n' ':')$(PATH)
endif

BINARY  := netbox-trueup-ai
MODULE  := github.com/zeddD1abl0/netbox-trueup-ai
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X $(MODULE)/internal/version.version=$(VERSION)

# NetBox release whose source `make reference` fetches (docs/adr/0003).
NETBOX_VERSION ?= 4.7.2
REFERENCE_DIR  := .cache/reference/netbox-$(NETBOX_VERSION)

.DEFAULT_GOAL := help

.PHONY: help
help: ## List available targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z_-]+:.*## / {printf "  %-10s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

.PHONY: tools
tools: ## Install the pinned toolchain with mise
	mise install

.PHONY: hooks
hooks: ## Install Git hooks (format, lint, secret scan, commit message)
	lefthook install

.PHONY: build
build: ## Build the binary into ./bin
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/$(BINARY)

.PHONY: test
test: ## Run tests with the race detector
	go test -race -shuffle=on ./...

.PHONY: fmt
fmt: ## Format Go code
	golangci-lint fmt

.PHONY: lint
lint: ## Lint Go code
	golangci-lint run

.PHONY: vuln
vuln: ## Report known vulnerabilities in code we call
	govulncheck ./...

.PHONY: lint-repo
lint-repo: ## Lint workflows and Markdown, and scan Git history for secrets
	actionlint
	zizmor .
	markdownlint-cli2
	gitleaks git --redact --no-banner

.PHONY: check
check: lint lint-repo test vuln build ## Run everything CI runs

.PHONY: reference
reference: ## Fetch NetBox source at NETBOX_VERSION into .cache/reference
	@if [ -d "$(REFERENCE_DIR)" ]; then \
		echo "$(REFERENCE_DIR) already exists"; \
	else \
		git clone --quiet --depth 1 --branch v$(NETBOX_VERSION) https://github.com/netbox-community/netbox.git "$(REFERENCE_DIR)"; \
		echo "NetBox $(NETBOX_VERSION) source is in $(REFERENCE_DIR)"; \
	fi

.PHONY: clean
clean: ## Remove build output
	rm -rf bin dist
