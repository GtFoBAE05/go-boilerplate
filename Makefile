.PHONY: run build test test-integration lint tidy sqlc sqlc-diff migrate-new migrate-up migrate-down migrate-status vuln docker-up docker-down docker-build help

# Tool versions are pinned so local runs and CI can never drift. Bump here and
# in .github/workflows/ci.yml together; its GOLANGCI_VERSION must match here.
# They are kept out of go.mod on purpose:
# sqlc's dependency tree (wazero, the tidb parser, cel-go) would otherwise be
# downloaded by the Dockerfile's `go mod download` on every image build.
GOLANGCI_VERSION ?= v2.15.0
GOOSE_VERSION ?= v3.27.3
SQLC_VERSION  ?= v1.30.0
VULN_VERSION  ?= v1.1.4
LINT := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)
GOOSE := go run github.com/pressly/goose/v3/cmd/goose@$(GOOSE_VERSION)
SQLC  := CGO_ENABLED=0 go run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)

DB_DSN ?= postgres://app:app@localhost:5433/app?sslmode=disable

# Build provenance, matching the Dockerfile's ldflags so a local binary reports
# the same fields as a container one.
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT     ?= $(shell git rev-parse HEAD 2>/dev/null || echo none)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X github.com/GtFoBAE05/go-boilerplate/internal/app.version=$(VERSION) \
	-X github.com/GtFoBAE05/go-boilerplate/internal/app.commit=$(COMMIT) \
	-X github.com/GtFoBAE05/go-boilerplate/internal/app.buildDate=$(BUILD_DATE)

run: ## Run the API locally
	go run ./cmd/api

build: ## Build the API binary into ./bin
	go build -trimpath -ldflags="$(LDFLAGS)" -o bin/api ./cmd/api

test: ## Run unit tests with race detector
	go test -race ./...

test-integration: ## Run all tests including integration ones (needs Docker)
	go test -race -tags integration ./...

lint: ## Run golangci-lint
	$(LINT) run

tidy: ## go mod tidy
	go mod tidy

sqlc: ## Regenerate type-safe query code
	$(SQLC) generate

sqlc-diff: ## Fail if generated code is stale (same check as CI)
	$(SQLC) diff

vuln: ## Scan dependencies for known vulnerabilities
	go run golang.org/x/vuln/cmd/govulncheck@$(VULN_VERSION) ./...

migrate-new: ## Create a new migration: make migrate-new name=add_foo
	$(GOOSE) -dir db/migrations create $(name) sql

migrate-up: ## Apply migrations to local db
	$(GOOSE) -dir db/migrations postgres "$(DB_DSN)" up

migrate-down: ## Roll back one migration
	$(GOOSE) -dir db/migrations postgres "$(DB_DSN)" down

migrate-status: ## Show which migrations are applied
	$(GOOSE) -dir db/migrations postgres "$(DB_DSN)" status

docker-up: ## Start postgres, redis, jaeger, prometheus
	docker compose up -d

docker-down: ## Stop the local stack
	docker compose down

docker-build: ## Build the production image with provenance stamped in
	docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-t go-boilerplate:$(VERSION) -t go-boilerplate:latest .

help: ## Show targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-16s %s\n", $$1, $$2}'
