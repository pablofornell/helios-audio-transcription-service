GO := export PATH="/usr/local/go/bin:$$HOME/go/bin:$$PATH"; go
WAILS := export PATH="/usr/local/go/bin:$$HOME/go/bin:$$PATH"; wails
BINARY := helios-transcription-api
DOCKER_IMAGE := helios-transcription-api
COMPOSE := docker compose -f deploy/docker-compose.yml
COMPOSE_VULKAN := $(COMPOSE) -f deploy/docker-compose.vulkan.yml
# Ubuntu 24.04 ships libwebkit2gtk-4.1-dev, not the 4.0 package Wails v2
# looks for by default; every desktop build/dev invocation needs this tag.
# It is also set as desktop/wails.json's own "build:tags", so this is
# belt-and-suspenders, not the only place it is declared.
DESKTOP_TAGS := webkit2_41

# `go ./...` walks desktop/frontend/node_modules, where at least one npm
# package ships a stray Go file. It is not ours, it is gitignored, and it
# has no business in our build -- but the go command only skips testdata
# and dot/underscore directories, so it must be filtered explicitly. CI is
# unaffected (its Go job never runs npm install), which is exactly why
# this would otherwise only ever bite someone locally.
PKGS = $$($(GO) list ./... | grep -v /node_modules/)

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help.
	@grep -E '^[a-zA-Z0-9_-]+:.*## ' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the helios-transcription-api binary.
	$(GO) build -o bin/$(BINARY) ./cmd/helios-transcription-api

.PHONY: test
test: ## Run the test suite.
	$(GO) test $(PKGS)

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
	$(GO) vet $(PKGS)

.PHONY: run
run: ## Run helios-transcription-api locally (reads HELIOS_* env vars).
	$(GO) run ./cmd/helios-transcription-api

.PHONY: migrate
migrate: ## Apply database migrations using HELIOS_DATABASE_DSN.
	$(GO) run ./cmd/helios-transcription-api --migrate-only

.PHONY: models
models: ## Download whisper.cpp ggml model weights into ./models (default: large-v3; override with `make models MODEL=base`).
	./scripts/fetch-models.sh $(MODEL)

.PHONY: docker-build
docker-build: ## Build the helios-transcription-api Docker image.
	docker build -f deploy/Dockerfile -t $(DOCKER_IMAGE) .

.PHONY: compose-up
compose-up: ## Start postgres + helios-transcription-api via docker compose.
	$(COMPOSE) up -d

.PHONY: compose-up-vulkan
compose-up-vulkan: ## Build/start AMD Vulkan stack (set HELIOS_RENDER_GID; see runbook).
	$(COMPOSE_VULKAN) up -d --build

.PHONY: compose-down
compose-down: ## Stop and remove the docker compose stack.
	$(COMPOSE) down

.PHONY: compose-logs
compose-logs: ## Tail docker compose logs.
	$(COMPOSE) logs -f

.PHONY: tidy
tidy: ## Run go mod tidy.
	$(GO) mod tidy

.PHONY: desktop-dev
desktop-dev: ## Run the Wails desktop app in dev mode (HELIOS_API_URL via env; defaults to http://localhost:7462).
	cd desktop && $(WAILS) dev -tags $(DESKTOP_TAGS)

.PHONY: desktop-build
desktop-build: ## Build the Wails desktop app binary (desktop/build/bin/helios-ats-desktop).
	cd desktop && $(WAILS) build -tags $(DESKTOP_TAGS)

.PHONY: desktop-run
desktop-run: desktop-build ## Build the production desktop binary and launch it (HELIOS_API_URL via env).
	./desktop/build/bin/helios-ats-desktop
