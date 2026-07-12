.PHONY: run build test test-integration lint tidy sqlc migrate-new migrate-up migrate-down docker-up docker-down docker-build help

run: ## Run the API locally
	go run ./cmd/api

build: ## Build the API binary into ./bin
	go build -o bin/api ./cmd/api

test: ## Run tests with race detector
	go test -race ./...

test-integration: ## Run integration tests (needs Docker)
	go test -race -tags integration ./...

lint: ## Run golangci-lint
	golangci-lint run

tidy: ## go mod tidy
	go mod tidy

sqlc: ## Regenerate type-safe query code (requires sqlc)
	sqlc generate

migrate-new: ## Create a new migration: make migrate-new name=add_foo
	go run github.com/pressly/goose/v3/cmd/goose@latest -dir db/migrations create $(name) sql

migrate-up: ## Apply migrations to local db
	go run github.com/pressly/goose/v3/cmd/goose@latest -dir db/migrations postgres "postgres://app:app@localhost:5433/app?sslmode=disable" up

migrate-down: ## Roll back one migration
	go run github.com/pressly/goose/v3/cmd/goose@latest -dir db/migrations postgres "postgres://app:app@localhost:5433/app?sslmode=disable" down

docker-up: ## Start postgres, redis, jaeger, prometheus
	docker compose up -d

docker-down: ## Stop the local stack
	docker compose down

docker-build: ## Build the production image
	docker build -t go-boilerplate:latest .

help: ## Show targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-14s %s\n", $$1, $$2}'
