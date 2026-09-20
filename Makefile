GO := export PATH="/usr/local/go/bin:$$HOME/go/bin:$$PATH"; go
BINARY := helios-transcription-api
DOCKER_IMAGE := helios-transcription-api
COMPOSE := docker compose -f deploy/docker-compose.yml

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help.
	@grep -E '^[a-zA-Z0-9_-]+:.*## ' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the helios-transcription-api binary.
	$(GO) build -o bin/$(BINARY) ./cmd/helios-transcription-api

.PHONY: test
test: ## Run the test suite.
	$(GO) test ./...

.PHONY: e2e
e2e: ## Run the in-process end-to-end suite (internal/e2e); needs Docker for its Postgres testcontainer.
	$(GO) test ./internal/e2e/... -race -count=1 -v

.PHONY: lint
lint: fmt vet ## Run fmt and vet (no external linter pinned yet).

.PHONY: fmt
fmt: ## Check formatting; fails if gofmt would change anything.
	@export PATH="/usr/local/go/bin:$$HOME/go/bin:$$PATH"; \
	unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt needs to be run on:"; echo "$$unformatted"; exit 1; \
	fi

.PHONY: vet
vet: ## Run go vet.
	$(GO) vet ./...

.PHONY: run
run: ## Run helios-transcription-api locally (reads HELIOS_* env vars).
	$(GO) run ./cmd/helios-transcription-api

.PHONY: migrate
migrate: ## Apply database migrations using HELIOS_DATABASE_DSN.
	$(GO) run ./cmd/helios-transcription-api --migrate-only

.PHONY: models
models: ## Download whisper.cpp ggml model weights into ./models (default: base; override with `make models MODEL=large-v3`).
	./scripts/fetch-models.sh $(MODEL)

.PHONY: docker-build
docker-build: ## Build the helios-transcription-api Docker image.
	docker build -f deploy/Dockerfile -t $(DOCKER_IMAGE) .

.PHONY: compose-up
compose-up: ## Start postgres + helios-transcription-api via docker compose.
	$(COMPOSE) up -d

.PHONY: compose-down
compose-down: ## Stop and remove the docker compose stack.
	$(COMPOSE) down

.PHONY: compose-logs
compose-logs: ## Tail docker compose logs.
	$(COMPOSE) logs -f

.PHONY: tidy
tidy: ## Run go mod tidy.
	$(GO) mod tidy
