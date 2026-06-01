-include .env
export

THE_MOMENT_DB_PATH      ?= ./the-moment-data/db
THE_MOMENT_GCODE_PATH   ?= ./the-moment-data/gcode
THE_MOMENT_UPLOADS_PATH ?= ./the-moment-data/uploads
THE_MOMENT_PORT         ?= 5000
SPOOLMAN_DB_PATH        ?= ./spoolman-data
BACKUP_DIR              ?= ./backups

.PHONY: setup up down logs update ps open backup restore \
        dev-build dev-up dev-down \
        test-unit test-integration test-all lint help

# ── Docker management ──────────────────────────────────────────────────────────

setup: ## First-time setup: copy .env.example → .env (if absent) and create data dirs
	@test -f .env || (cp .env.example .env && echo "Created .env from .env.example — review ports and TZ before continuing")
	mkdir -p $(THE_MOMENT_DB_PATH) $(THE_MOMENT_GCODE_PATH) $(THE_MOMENT_UPLOADS_PATH) $(SPOOLMAN_DB_PATH)
	@echo "Ready. Run 'make up' to start."

up: ## Create data directories and start all services
	mkdir -p $(THE_MOMENT_DB_PATH) $(THE_MOMENT_GCODE_PATH) $(THE_MOMENT_UPLOADS_PATH) $(SPOOLMAN_DB_PATH)
	docker compose up -d

down: ## Stop all services
	docker compose down

logs: ## Tail logs from all services (Ctrl-C to stop)
	docker compose logs -f

update: ## Pull latest images, create dirs, and restart
	docker compose pull
	mkdir -p $(THE_MOMENT_DB_PATH) $(THE_MOMENT_GCODE_PATH) $(THE_MOMENT_UPLOADS_PATH) $(SPOOLMAN_DB_PATH)
	docker compose up -d

ps: ## Show running containers and their status
	docker compose ps

open: ## Open the The Moment web UI in the default browser
	@open "http://localhost:$(THE_MOMENT_PORT)" 2>/dev/null || \
	 xdg-open "http://localhost:$(THE_MOMENT_PORT)" 2>/dev/null || \
	 echo "Open http://localhost:$(THE_MOMENT_PORT) in your browser"

# ── Data management ────────────────────────────────────────────────────────────

backup: ## Stop services, archive data + config to BACKUP_DIR, restart
	@mkdir -p $(BACKUP_DIR)
	docker compose stop
	@set -e; \
	 out="$(BACKUP_DIR)/backup-$$(date +%Y%m%d-%H%M%S).tar.gz"; \
	 extras=""; [ -f .env ] && extras=".env"; \
	 tar -czf "$$out" \
	     $(THE_MOMENT_DB_PATH) $(THE_MOMENT_GCODE_PATH) $(THE_MOMENT_UPLOADS_PATH) $(SPOOLMAN_DB_PATH) \
	     docker-compose.yml Makefile $$extras; \
	 echo "Backup saved: $$out"
	docker compose start

restore: ## Restore from a backup: make restore BACKUP=./backups/backup-YYYYMMDD-HHMMSS.tar.gz
	@test -n "$(BACKUP)" || { echo "Error: specify a file — make restore BACKUP=<path>"; exit 1; }
	@test -f "$(BACKUP)" || { echo "Error: file not found: $(BACKUP)"; exit 1; }
	docker compose stop
	tar -xzf $(BACKUP)
	docker compose start
	@echo "Restored from $(BACKUP)"

# ── Development ────────────────────────────────────────────────────────────────

DEV_COMPOSE = docker compose -f docker-compose.yml -f docker-compose.dev.yml

dev-build: ## Build the dev image (run once; re-run if go.mod changes)
	$(DEV_COMPOSE) build the-moment

dev-up: ## Start dev stack with air hot-reload (foreground — Ctrl-C to stop)
	mkdir -p $(THE_MOMENT_DB_PATH) $(THE_MOMENT_GCODE_PATH) $(THE_MOMENT_UPLOADS_PATH) $(SPOOLMAN_DB_PATH)
	$(DEV_COMPOSE) up

dev-down: ## Stop dev stack
	$(DEV_COMPOSE) down

# ── Quality ────────────────────────────────────────────────────────────────────

test-unit: ## Run unit tests (no build tag required, fast, no external deps)
	go test ./... -count=1

test-integration: ## Run integration tests (requires build tag; spins up in-process DB)
	go test -tags=integration ./... -count=1 -v

test-all: test-unit test-integration ## Run unit tests then integration tests

lint: ## Run go vet and staticcheck (install: go install honnef.co/go/tools/cmd/staticcheck@latest)
	go vet ./...
	@command -v staticcheck >/dev/null 2>&1 && staticcheck ./... || echo "staticcheck not installed — skipping"

# ── Help ───────────────────────────────────────────────────────────────────────

help: ## Show available targets
	@echo ""
	@echo "  The Moment — Makefile targets"
	@echo ""
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' Makefile | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  %-18s %s\n", $$1, $$2}'
	@echo ""

.DEFAULT_GOAL := help
