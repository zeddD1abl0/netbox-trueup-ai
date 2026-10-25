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

# NetBox release whose source `make reference` fetches (docs/adr/0003). Keep it
# in step with netboxtest.Version and the image in internal/netboxtest/compose.yaml.
NETBOX_VERSION ?= 4.7.2
REFERENCE_DIR  := .cache/reference/netbox-$(NETBOX_VERSION)

# The disposable local NetBox (internal/netboxtest/README.md). Only ever on
# 127.0.0.1; set NETBOX_PORT to use another port.
NETBOX_PORT       ?= 8000
NETBOX_COMPOSE    := docker compose --file internal/netboxtest/compose.yaml
NETBOX_TOKEN_FILE := .cache/netbox/token

.DEFAULT_GOAL := help

.PHONY: help
help: ## List available targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z_-]+:.*## / {printf "  %-16s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

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

# Each package with integration tests starts its own NetBox, which takes
# about 4 minutes and 1 GB, so packages run one at a time (-p 1). The long
# timeout applies to each package.
.PHONY: test-integration
test-integration: ## Run tests and integration tests against a disposable NetBox (needs Docker)
	go test -race -shuffle=on -tags integration -p 1 -timeout 30m ./...

.PHONY: netbox-up
netbox-up: ## Start a disposable NetBox on 127.0.0.1 (port NETBOX_PORT, default 8000) and provision a token
	NETBOX_PORT=$(NETBOX_PORT) $(NETBOX_COMPOSE) up --detach --wait --wait-timeout 1200
	go run ./internal/netboxtest/cmd/netbox-token -url http://127.0.0.1:$(NETBOX_PORT) -out $(NETBOX_TOKEN_FILE)

.PHONY: netbox-down
netbox-down: ## Stop the disposable NetBox and delete its data
	$(NETBOX_COMPOSE) down --volumes --remove-orphans
	rm -f $(NETBOX_TOKEN_FILE)

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
check: lint lint-repo test vuln build ## Run everything CI runs but the integration tests

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
