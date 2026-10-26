.PHONY: help up down run build test test-integration lint sample stack stack-logs
.DEFAULT_GOAL := help

# Load .env (API keys, LLM_PROVIDER, ...) into the environment of recipes that need it.
ENV = set -a; [ -f .env ] && . ./.env; set +a;

help: ## Show the available targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "}; {printf "  make %-17s %s\n", $$1, $$2}'

up: ## Start Qdrant and Redis in Docker (for `make run`)
	docker compose up -d qdrant redis

down: ## Stop all containers
	docker compose down

run: ## Run the API on the host at http://localhost:8080 (reads .env)
	$(ENV) go run ./cmd/server

build: ## Build the binary into bin/
	go build -o bin/askyourbook ./cmd/server

test: ## Unit tests with the race detector (no network or API key)
	go test -race ./...

# Set TEST_REDIS_URL=redis://localhost:6379/15 to include Redis.
test-integration: ## Integration tests against Qdrant/Redis from `make up`
	go test -tags integration -race -count=1 ./internal/integration ./internal/session

lint: ## gofmt and go vet
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:"; gofmt -l .; exit 1)
	go vet ./...

sample: ## Regenerate testdata/sample.pdf
	python3 testdata/gen_sample_pdf.py

stack: ## Build and start everything in Docker: API + Qdrant + Redis
	docker compose up -d --build

stack-logs: ## Follow the API container's logs
	docker compose logs -f api
