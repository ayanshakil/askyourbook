.PHONY: up down run build test lint sample stack stack-logs

# Load .env (API keys, LLM_PROVIDER, ...) into the environment of recipes that need it.
ENV = set -a; [ -f .env ] && . ./.env; set +a;

# Dependencies only (Qdrant, Redis); run the API on the host with `make run`.
up:
	docker compose up -d qdrant redis

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

# Everything in containers: API + Qdrant + Redis.
stack:
	docker compose up -d --build

stack-logs:
	docker compose logs -f api
