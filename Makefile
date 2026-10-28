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

# The packages sqlc generates, as sqlc.yaml names them (its out: lines). They
# hold nothing but generated files.
SQLC_OUT := $(shell sed -n 's/^[[:space:]]*out:[[:space:]]*//p' sqlc.yaml)

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
# about 4 minutes and 1 GB, or its own PostgreSQL, which takes seconds, so
# packages run one at a time (-p 1). The long timeout applies to each
# package.
.PHONY: test-integration
test-integration: ## Run tests and integration tests against a disposable NetBox and PostgreSQL (needs Docker)
	go test -race -shuffle=on -tags integration -p 1 -timeout 30m ./...

.PHONY: netbox-up
netbox-up: ## Start a disposable NetBox on 127.0.0.1 (port NETBOX_PORT, default 8000) and provision a token
	NETBOX_PORT=$(NETBOX_PORT) $(NETBOX_COMPOSE) up --detach --wait --wait-timeout 1200
	go run ./internal/netboxtest/cmd/netbox-token -url http://127.0.0.1:$(NETBOX_PORT) -out $(NETBOX_TOKEN_FILE)

.PHONY: netbox-down
netbox-down: ## Stop the disposable NetBox and delete its data
	$(NETBOX_COMPOSE) down --volumes --remove-orphans
	rm -f $(NETBOX_TOKEN_FILE)

# SQLC_FRESH generates the sqlc packages afresh into the directory "$tmp",
# from a copy of sqlc.yaml and internal/store, leaving the tree alone.
SQLC_FRESH = mkdir -p "$$tmp/internal" && cp sqlc.yaml "$$tmp/" && cp -R internal/store "$$tmp/internal/" && \
	for d in $(SQLC_OUT); do rm -f "$$tmp/$$d"/*.go; done && \
	sqlc generate --no-remote --file "$$tmp/sqlc.yaml"

# The packages are replaced whole, so that a deleted query file leaves no
# file behind, and only once sqlc has succeeded, so that a mistake in the
# SQL leaves the old code in place.
.PHONY: generate
generate: ## Regenerate the Go code generated from SQL (sqlc)
	@tmp=$$(mktemp -d) && trap 'rm -rf "$$tmp"' EXIT && $(SQLC_FRESH) && \
	for d in $(SQLC_OUT); do mkdir -p "$$d" && rm -f "$$d"/*.go && cp "$$tmp/$$d"/*.go "$$d"/; done

# Compares fresh output with the tree, so that a changed, missing or extra
# file fails.
.PHONY: generate-check
generate-check: ## Check that the generated code matches its sources
	@tmp=$$(mktemp -d) && trap 'rm -rf "$$tmp"' EXIT && $(SQLC_FRESH) && \
	for d in $(SQLC_OUT); do \
		diff -r "$$d" "$$tmp/$$d" || { echo "$$d doesn't match the SQL it is generated from: run make generate and commit the result"; exit 1; }; \
	done

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
check: lint lint-repo generate-check test vuln build ## Run everything CI runs but the integration tests

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
