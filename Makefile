.PHONY: up down run build test lint sample

# Load .env (API keys, LLM_PROVIDER, ...) into the environment of recipes that need it.
ENV = set -a; [ -f .env ] && . ./.env; set +a;

up:
	docker compose up -d

down:
	docker compose down

run:
	$(ENV) go run ./cmd/server

build:
	go build -o bin/askyourbook ./cmd/server

test:
	go test -race ./...

lint:
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:"; gofmt -l .; exit 1)
	go vet ./...

sample:
	python3 testdata/gen_sample_pdf.py
