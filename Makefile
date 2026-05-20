-include .env
export

THE_MOMENT_DB_PATH ?= ./the-moment-data
SPOOLMAN_DB_PATH   ?= ./spoolman-data
BACKUP_DIR         ?= ./backups

.PHONY: up down logs update ps backup restore test-unit test-integration test-all lint help

up: ## Create data directories and start all services
	mkdir -p $(THE_MOMENT_DB_PATH) $(SPOOLMAN_DB_PATH)
	docker compose up -d

down: ## Stop all services
	docker compose down

logs: ## Tail logs from all services (Ctrl-C to stop)
	docker compose logs -f

update: ## Pull latest images, create dirs, and restart
	docker compose pull
	mkdir -p $(THE_MOMENT_DB_PATH) $(SPOOLMAN_DB_PATH)
	docker compose up -d

ps: ## Show running containers and their status
	docker compose ps

backup: ## Stop services, archive data + config to BACKUP_DIR, restart
	@mkdir -p $(BACKUP_DIR)
	docker compose stop
	@set -e; \
	 out="$(BACKUP_DIR)/backup-$$(date +%Y%m%d-%H%M%S).tar.gz"; \
	 extras=""; [ -f .env ] && extras=".env"; \
	 tar -czf "$$out" \
	     $(THE_MOMENT_DB_PATH) $(SPOOLMAN_DB_PATH) \
	     docker-compose.yml Makefile $$extras; \
	 echo "Backup saved: $$out"
	docker compose start

test-unit: ## Run unit tests (no build tag required, fast, no external deps)
	go test ./... -count=1

test-integration: ## Run integration tests (requires build tag; spins up in-process DB)
	go test -tags=integration ./... -count=1 -v

test-all: test-unit test-integration ## Run unit tests then integration tests

lint: ## Run go vet and staticcheck (install staticcheck: go install honnef.co/go/tools/cmd/staticcheck@latest)
	go vet ./...
	@command -v staticcheck >/dev/null 2>&1 && staticcheck ./... || echo "staticcheck not installed — skipping (go install honnef.co/go/tools/cmd/staticcheck@latest)"

restore: ## Restore from a backup: make restore BACKUP=./backups/backup-YYYYMMDD-HHMMSS.tar.gz
	@test -n "$(BACKUP)" || { echo "Error: specify a file — make restore BACKUP=<path>"; exit 1; }
	@test -f "$(BACKUP)" || { echo "Error: file not found: $(BACKUP)"; exit 1; }
	docker compose stop
	tar -xzf $(BACKUP)
	docker compose start
	@echo "Restored from $(BACKUP)"

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  %-10s %s\n", $$1, $$2}'

.DEFAULT_GOAL := help
