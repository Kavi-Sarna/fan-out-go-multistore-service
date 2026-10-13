SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help

BINARY  := bin/api
PKG     := ./...
COMPOSE := docker compose --file infra/docker/compose.yaml

# Tooling is pinned and installed into .tools rather than assumed to be on
# PATH. `make lint` failing with "command not found" on a machine that has
# Go is a bad first five minutes, and two people running different linter
# versions is a worse afternoon.
TOOLS  := $(CURDIR)/.tools
MODULE := $(shell go list -m)

# The versions live in one file that both make and ci/run.sh read, because a
# pin declared twice is the drift this project exists to avoid.
include ci/tools.env

# Keyed by version, so bumping a pin installs the new one rather than finding
# a file of the right name and running whatever version it happens to be.
GOLANGCI  := $(TOOLS)/golangci-lint-$(GOLANGCI_VER)
GOIMPORTS := $(TOOLS)/goimports-$(GOIMPORTS_VER)

.PHONY: help fmt vet lint test race cover build run tidy tools ci scan image up down logs clean clean-tools

help: ## Show this help
	@grep -hE '^[a-z0-9-]+:.*?## ' $(MAKEFILE_LIST) \
	  | awk -F':.*?## ' '{printf "  \033[36m%-8s\033[0m %s\n", $$1, $$2}'

# Installed via a scratch GOBIN then renamed, because `go install` names the
# binary after the package and cannot be told to add a version suffix.
$(GOLANGCI):
	@echo "installing golangci-lint $(GOLANGCI_VER)"
	@mkdir -p $(TOOLS) && tmp=$$(mktemp -d) && \
	  GOBIN=$$tmp go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VER) && \
	  mv $$tmp/golangci-lint $@ && rm -rf $$tmp

$(GOIMPORTS):
	@echo "installing goimports $(GOIMPORTS_VER)"
	@mkdir -p $(TOOLS) && tmp=$$(mktemp -d) && \
	  GOBIN=$$tmp go install golang.org/x/tools/cmd/goimports@$(GOIMPORTS_VER) && \
	  mv $$tmp/goimports $@ && rm -rf $$tmp

tools: $(GOLANGCI) $(GOIMPORTS) ## Install the pinned tools into .tools
	@ln -sf $(notdir $(GOLANGCI)) $(TOOLS)/golangci-lint
	@ln -sf $(notdir $(GOIMPORTS)) $(TOOLS)/goimports

fmt: $(GOIMPORTS) ## Format the code
	$(GOIMPORTS) -w -local $(MODULE) .
	gofmt -s -w .

vet: ## Report suspicious constructs
	go vet $(PKG)

lint: $(GOLANGCI) ## Run the linters
	$(GOLANGCI) run

test: ## Run the tests
	go test $(PKG)

race: ## Run the tests under the race detector
	go test -race $(PKG)

ci: ## Run everything CI runs, in the same order, by the same script
	./ci/run.sh

scan: ## Known vulnerabilities in the code, its dependencies, the stdlib and the image
	./ci/scan.sh

image: ## Build the container image
	$(COMPOSE) build

up: ## Run the service in a container on :8081, and wait until it is healthy
	$(COMPOSE) up --build --detach --wait
	@echo "the service is on http://localhost:8081/healthz"

down: ## Stop the containers
	$(COMPOSE) down --remove-orphans

logs: ## Follow the container logs
	$(COMPOSE) logs --follow

cover: ## Run the tests and report total coverage
	go test -coverprofile=coverage.out $(PKG)
	go tool cover -func=coverage.out | tail -1

build: ## Build the binary
	go build -o $(BINARY) ./cmd/api

run: ## Run the service. ADDR=:9090 to move it
	ADDR=$(ADDR) go run ./cmd/api

tidy: ## Tidy go.mod and go.sum in place
	go mod tidy

clean: ## Remove build artifacts
	rm -rf bin coverage.out

clean-tools: ## Remove the pinned tools, forcing a reinstall
	rm -rf $(TOOLS)
