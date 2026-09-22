-include .env
export

THE_MOMENT_DB_PATH      ?= ./the-moment-data/db
THE_MOMENT_GCODE_PATH   ?= ./the-moment-data/gcode
THE_MOMENT_UPLOADS_PATH ?= ./the-moment-data/uploads
THE_MOMENT_PORT         ?= 5000
SPOOLMAN_DB_PATH        ?= ./spoolman-data
BACKUP_DIR              ?= ./backups

.PHONY: setup up down logs update ps open backup restore backup-native restore-native \
        dev-build dev-up dev-down \
        test-unit test-integration test-all lint \
        version changelog-preview \
        github-push github-release github-release-beta github-dispatch private-add \
        pr-fetch pr-try pr-try-clean pr-merge help

# ── Docker management ──────────────────────────────────────────────────────────

setup: ## First-time setup: copy .env.example → .env (if absent) and create data dirs
	@test -f .env || (cp .env.example .env && echo "Created .env from .env.example — review ports and TZ before continuing")
	mkdir -p $(THE_MOMENT_DB_PATH) $(THE_MOMENT_GCODE_PATH) $(THE_MOMENT_UPLOADS_PATH) $(SPOOLMAN_DB_PATH) $(BACKUP_DIR)
	@echo "Ready. Run 'make up' to start."

up: ## Create data directories and start all services
	mkdir -p $(THE_MOMENT_DB_PATH) $(THE_MOMENT_GCODE_PATH) $(THE_MOMENT_UPLOADS_PATH) $(SPOOLMAN_DB_PATH) $(BACKUP_DIR)
	docker compose up -d

down: ## Stop all services
	docker compose down

logs: ## Tail logs from all services (Ctrl-C to stop)
	docker compose logs -f

update: ## Pull latest images, create dirs, and restart
	docker compose pull
	mkdir -p $(THE_MOMENT_DB_PATH) $(THE_MOMENT_GCODE_PATH) $(THE_MOMENT_UPLOADS_PATH) $(SPOOLMAN_DB_PATH) $(BACKUP_DIR)
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

backup-native: ## (Native only) Archive The Moment data to BACKUP_DIR. SCOPE=all|db|gcode|uploads (default: db)
	@mkdir -p $(BACKUP_DIR)
	$(eval _SCOPE := $(if $(SCOPE),$(SCOPE),db))
	@set -e; \
	 out="$(BACKUP_DIR)/the-moment-backup-$$(date +%Y%m%d-%H%M%S)-$(_SCOPE).tar.gz"; \
	 case "$(_SCOPE)" in \
	   all)     tar -czf "$$out" $(THE_MOMENT_DB_PATH) $(THE_MOMENT_GCODE_PATH) $(THE_MOMENT_UPLOADS_PATH) ;; \
	   db)      tar -czf "$$out" $(THE_MOMENT_DB_PATH) ;; \
	   gcode)   tar -czf "$$out" $(THE_MOMENT_GCODE_PATH) ;; \
	   uploads) tar -czf "$$out" $(THE_MOMENT_UPLOADS_PATH) ;; \
	   *)       echo "Error: unknown scope '$(_SCOPE)' — use all, db, gcode, or uploads"; exit 1 ;; \
	 esac; \
	 echo "Backup saved: $$out"

restore-native: ## (Native only) Restore data — STOP the binary first: make restore-native BACKUP=<path>
	@test -n "$(BACKUP)" || { echo "Error: specify BACKUP=<path>"; exit 1; }
	@test -f "$(BACKUP)" || { echo "Error: not found: $(BACKUP)"; exit 1; }
	@echo "WARNING: This will overwrite existing data directories."
	@echo "Press Enter to continue or Ctrl-C to cancel."; read _
	tar -xzf "$(BACKUP)" --overwrite
	@echo "Restored from $(BACKUP). Start the moment binary to use the restored data."

# ── Development ────────────────────────────────────────────────────────────────

DEV_COMPOSE = docker compose -f docker-compose.yml -f docker-compose.dev.yml

dev-build: ## Build the dev image (run once; re-run if go.mod changes)
	$(DEV_COMPOSE) build the-moment

dev-up: ## Start dev stack with air hot-reload (foreground — Ctrl-C to stop)
	mkdir -p $(THE_MOMENT_DB_PATH) $(THE_MOMENT_GCODE_PATH) $(THE_MOMENT_UPLOADS_PATH) $(SPOOLMAN_DB_PATH) $(BACKUP_DIR)
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

# ── Version & changelog ────────────────────────────────────────────────────────

version: ## Show current version, recent tags, and commits since last stable tag
	@echo "Current (version.go): v$(shell grep AppVersion version.go | grep -oE '"[^"]+"' | tr -d '"')"
	@echo ""
	@echo "Recent tags (newest first):"
	@git tag --sort=-creatordate | grep -v '\-src' | head -5
	@echo ""
	@LAST_TAG=$$(git tag --sort=-creatordate | grep -v '\-src' | grep -vE '\-beta\.|\-rc\.' | head -1); \
	 if [ -n "$$LAST_TAG" ]; then \
	     echo "Commits since last stable tag ($$LAST_TAG):"; \
	     git log $$LAST_TAG..HEAD --oneline | head -20; \
	 else \
	     echo "No stable tags found."; \
	 fi

changelog-preview: ## Draft CHANGELOG entry for commits since last stable tag
	@LAST_TAG=$$(git tag --sort=-creatordate | grep -v '\-src' | grep -vE '\-beta\.|\-rc\.' | head -1); \
	 if [ -z "$$LAST_TAG" ]; then echo "No stable tag found."; exit 1; fi; \
	 echo "## [vNEXT] - $$(date +%Y-%m-%d)"; echo ""; \
	 echo "### Added"; \
	 git log $$LAST_TAG..HEAD --oneline | grep -iE '^[a-f0-9]+ feat:' | sed 's/^[a-f0-9]* feat: /- /'; \
	 echo ""; \
	 echo "### Fixed"; \
	 git log $$LAST_TAG..HEAD --oneline | grep -iE '^[a-f0-9]+ fix:' | sed 's/^[a-f0-9]* fix: /- /'; \
	 echo ""; \
	 echo "### Changed (CI/chore -- may omit from release notes):"; \
	 git log $$LAST_TAG..HEAD --oneline | grep -iE '^[a-f0-9]+ (chore|ci|refactor):' | sed 's/^[a-f0-9]* [a-z]*: /- /'

# ── GitHub publishing ──────────────────────────────────────────────────────────

github-push-check: ## Dry-run: verify all private_files are excluded before github-push
	@echo "Simulating github-push to verify private_files exclusion..."
	@CURRENT_BRANCH=$$(git branch --show-current); \
	 git fetch origin -q; \
	 STASHED=0; \
	 if [ -n "$$(git status --porcelain)" ]; then \
	     git stash push --include-untracked -m "github-push-check" >/dev/null && STASHED=1; \
	 fi; \
	 git branch -D github-check 2>/dev/null; true; \
	 git checkout -b github-check origin/main; \
	 git checkout "$$CURRENT_BRANCH" -- .; \
	 if [ -f private_files ]; then \
	     while IFS= read -r f || [ -n "$$f" ]; do \
	         [ -n "$$f" ] && git rm -r --cached --force "$$f" 2>/dev/null; true; \
	     done < private_files; \
	 fi; \
	 FAIL=0; if [ -f private_files ]; then \
	     while IFS= read -r f || [ -n "$$f" ]; do \
	         [ -z "$$f" ] && continue; \
	         if git diff --cached --name-only | grep -qF "$$f"; then \
	             echo "  FAIL: $$f would leak to GitHub!"; FAIL=1; \
	         else \
	             echo "  OK:   $$f excluded"; \
	         fi; \
	     done < private_files; \
	 fi; \
	 git checkout -f "$$CURRENT_BRANCH"; \
	 git branch -D github-check 2>/dev/null; true; \
	 if [ "$$STASHED" = "1" ]; then \
	     git stash pop || { echo "ERROR: stash pop failed — run 'git stash list' and restore manually"; exit 1; }; \
	 fi; \
	 if [ "$$FAIL" = "1" ]; then \
	     echo "FAIL: private_files check failed — fix before running github-push"; \
	     exit 1; \
	 fi; \
	 echo "PASS: All private_files correctly excluded."

github-push: github-push-check ## Build public commit from main (private_files excluded) and push forward onto origin/main
	@echo "Building public commit from main (excluding private files)..."
	@CURRENT_BRANCH=$$(git branch --show-current); \
	 git fetch origin; \
	 STASHED=0; \
	 if [ -n "$$(git status --porcelain)" ]; then \
	     git stash push --include-untracked -m "github-push" >/dev/null && STASHED=1; \
	 fi; \
	 git branch -D github 2>/dev/null; true; \
	 git checkout -b github origin/main; \
	 git checkout "$$CURRENT_BRANCH" -- .; \
	 if [ -f private_files ]; then \
	     while IFS= read -r f || [ -n "$$f" ]; do \
	         [ -n "$$f" ] && git rm -r --cached --force "$$f" 2>/dev/null; true; \
	     done < private_files; \
	 fi; \
	 git commit -m "The Moment v$(shell grep AppVersion version.go | grep -oE '"[^"]+"' | tr -d '"')"; \
	 git push origin github:main; \
	 git checkout -f "$$CURRENT_BRANCH"; \
	 git branch -D github 2>/dev/null; true; \
	 if [ "$$STASHED" = "1" ]; then \
	     git stash pop || { echo "ERROR: stash pop failed — run 'git stash list' and restore manually"; exit 1; }; \
	 fi; \
	 echo "GitHub origin/main updated."

github-release: ## github-push then tag vX.Y.Z and create stable GitHub Release
	@VERSION=v$(shell grep AppVersion version.go | grep -oE '"[^"]+"' | tr -d '"') && \
	 if git tag | grep -qx "$$VERSION"; then \
	     echo ""; \
	     echo "  ERROR: $$VERSION is already tagged."; \
	     echo "  Update version.go before running github-release."; \
	     echo ""; \
	     exit 1; \
	 fi
	$(MAKE) github-push
	@VERSION=v$(shell grep AppVersion version.go | grep -oE '"[^"]+"' | tr -d '"') && \
	 git fetch origin && \
	 git tag $$VERSION origin/main && \
	 git push origin $$VERSION && \
	 git tag $$VERSION-src && \
	 git push local $$VERSION $$VERSION-src && \
	 NOTES=$$(awk "/^## \[$$VERSION\]/{found=1; next} found && /^## \[/{exit} found{print}" CHANGELOG.md) && \
	 gh release create $$VERSION \
	     --repo ThetaSigmaLabs/the-moment \
	     --title "The Moment $$VERSION" \
	     --notes "$$NOTES" && \
	 echo "Tagged $$VERSION and created GitHub Release." && \
	 echo "Actions: https://github.com/ThetaSigmaLabs/the-moment/actions"

github-release-beta: ## github-push then tag vX.Y.Z-beta.N as a GitHub pre-release
	@VERSION=v$(shell grep AppVersion version.go | grep -oE '"[^"]+"' | tr -d '"') && \
	 if git tag | grep -qx "$$VERSION"; then \
	     echo ""; \
	     echo "  ERROR: $$VERSION is already tagged."; \
	     echo "  Update version.go before running github-release-beta."; \
	     echo ""; \
	     exit 1; \
	 fi
	$(MAKE) github-push
	@VERSION=v$(shell grep AppVersion version.go | grep -oE '"[^"]+"' | tr -d '"') && \
	 git fetch origin && \
	 git tag $$VERSION origin/main && \
	 git push origin $$VERSION && \
	 git tag $$VERSION-src && \
	 git push local $$VERSION $$VERSION-src && \
	 NOTES=$$(awk "/^## \[$$VERSION\]/{found=1; next} found && /^## \[/{exit} found{print}" CHANGELOG.md) && \
	 gh release create $$VERSION \
	     --repo ThetaSigmaLabs/the-moment \
	     --title "The Moment $$VERSION" \
	     --notes "$$NOTES" \
	     --prerelease && \
	 echo "Pre-release $$VERSION published." && \
	 echo "Actions: https://github.com/ThetaSigmaLabs/the-moment/actions"

github-dispatch: ## Push current work to GitHub and trigger a test Docker build (SHA tag only)
	$(MAKE) github-push
	gh workflow run docker-build.yml --repo ThetaSigmaLabs/the-moment
	@echo "Test build triggered. Find the SHA at:"
	@echo "  https://github.com/ThetaSigmaLabs/the-moment/actions"

# ── Contributor PRs ────────────────────────────────────────────────────────────

pr-fetch: ## Fetch a GitHub PR into a test branch and run tests: make pr-fetch PR=<number>
	@test -n "$(PR)" || { echo "Usage: make pr-fetch PR=<number>"; exit 1; }
	@echo "Fetching PR #$(PR) into branch pr-$(PR)..."
	@STASHED=0; \
	 if [ -n "$$(git status --porcelain)" ]; then \
	     git stash push --include-untracked -m "pr-fetch-$(PR)" >/dev/null && STASHED=1; \
	 fi; \
	 git fetch origin pull/$(PR)/head:pr-$(PR); \
	 git checkout pr-$(PR); \
	 go test ./...; \
	 git checkout main; \
	 if [ "$$STASHED" = "1" ]; then \
	     git stash pop || { echo "ERROR: stash pop failed — run 'git stash list' and restore manually"; exit 1; }; \
	 fi; \
	 echo ""; \
	 echo "PR #$(PR) fetched and tested. You are back on main."; \
	 echo "Review: git checkout pr-$(PR)"; \
	 echo "When ready: merge on GitHub, then run: make pr-merge PR=$(PR)"

pr-try: ## Replay a PR onto a temp branch cut from local main: make pr-try PR=<number>
	@test -n "$(PR)" || { echo "Usage: make pr-try PR=<number>"; exit 1; }
	@test -z "$$(git status --porcelain)" || { \
	     echo ""; \
	     echo "  ERROR: working tree is dirty."; \
	     echo ""; \
	     echo "  pr-try leaves you on branch pr-try-$(PR), not main, so it will not"; \
	     echo "  stash for you. A main-based stash popped onto the PR branch can"; \
	     echo "  conflict on the same files the PR touches, and you would not be"; \
	     echo "  able to tell your own edits from the contributor's."; \
	     echo ""; \
	     echo "  Commit or stash your work first, then re-run."; \
	     echo ""; \
	     exit 1; \
	 }
	@git show-ref --verify --quiet refs/heads/pr-try-$(PR) && { \
	     echo ""; \
	     echo "  ERROR: branch pr-try-$(PR) already exists."; \
	     echo "  It may hold conflict resolutions from an earlier run."; \
	     echo "  Discard it with: make pr-try-clean PR=$(PR)"; \
	     echo ""; \
	     exit 1; \
	 } || true
	@echo "Replaying PR #$(PR) onto a branch cut from local main..."
	@git fetch origin pull/$(PR)/head; \
	 COMMITS=$$(gh pr view $(PR) --repo ThetaSigmaLabs/the-moment \
	     --json commits --jq '.commits[].oid' | tr '\n' ' '); \
	 [ -z "$$COMMITS" ] && { echo "ERROR: could not fetch PR #$(PR) commit list"; exit 1; }; \
	 git checkout -b pr-try-$(PR) main || exit 1; \
	 echo "Cherry-picking: $$COMMITS"; \
	 git cherry-pick $$COMMITS; RC=$$?; \
	 if [ $$RC -ne 0 ]; then \
	     if git diff --name-only --diff-filter=U | grep -q .; then \
	         echo ""; \
	         echo "  CONFLICT: the PR does not apply cleanly to current main."; \
	         echo "  You are on pr-try-$(PR) mid-cherry-pick. Resolve, then:"; \
	         echo "      git cherry-pick --continue"; \
	         echo "  Or abandon the evaluation:"; \
	         echo "      git cherry-pick --abort && make pr-try-clean PR=$(PR)"; \
	         echo ""; \
	         echo "  The same conflict will occur in pr-merge after the GitHub merge."; \
	         exit 1; \
	     else \
	         git cherry-pick --skip 2>/dev/null; \
	         echo "NOTE: PR #$(PR) commits already present in local main."; \
	     fi; \
	 fi; \
	 go test ./...; TRC=$$?; \
	 echo ""; \
	 if [ $$TRC -ne 0 ]; then \
	     echo "  TESTS FAILED on PR #$(PR) combined with local main."; \
	 else \
	     echo "  Tests passed on PR #$(PR) combined with local main."; \
	 fi; \
	 echo "  You are on pr-try-$(PR). Build and run the app here to evaluate."; \
	 echo "  A clean result means the commits apply and tests pass. It does not"; \
	 echo "  rule out behaviour conflicts where both sides changed related logic."; \
	 echo "  When done: make pr-try-clean PR=$(PR)"; \
	 echo ""; \
	 exit $$TRC

pr-try-clean: ## Return to main and delete the pr-try branch: make pr-try-clean PR=<number>
	@test -n "$(PR)" || { echo "Usage: make pr-try-clean PR=<number>"; exit 1; }
	@git checkout main
	@git branch -D pr-try-$(PR) 2>/dev/null || echo "No branch pr-try-$(PR) to delete."
	@echo "Back on main. pr-try-$(PR) removed; local main untouched."

pr-merge: ## Cherry-pick a merged PR onto local main: make pr-merge PR=<number>
	@test -n "$(PR)" || { echo "Usage: make pr-merge PR=<number>"; exit 1; }
	@git checkout main; \
	 echo "Fetching commit list for PR #$(PR) from GitHub..."; \
	 COMMITS=$$(gh pr view $(PR) --repo ThetaSigmaLabs/the-moment \
	     --json commits --jq '.commits[].oid' | tr '\n' ' '); \
	 [ -z "$$COMMITS" ] && { echo "ERROR: could not fetch PR #$(PR) commit list"; exit 1; }; \
	 echo "Cherry-picking: $$COMMITS"; \
	 git cherry-pick $$COMMITS; RC=$$?; \
	 if [ $$RC -ne 0 ]; then \
	     if git diff --name-only --diff-filter=U | grep -q .; then \
	         echo "ERROR: cherry-pick conflict — resolve then run: git cherry-pick --continue"; exit 1; \
	     else \
	         git cherry-pick --skip 2>/dev/null; \
	         echo "NOTE: PR #$(PR) commits already present in local main — nothing to do."; \
	     fi; \
	 else \
	     git branch -D pr-$(PR) 2>/dev/null; true; \
	     echo "PR #$(PR) cherry-picked onto local main."; \
	     echo "Next: go test ./... and update CHANGELOG.md with contributor credit."; \
	 fi

private-add: ## Mark FILE=<path> as private (excluded from GitHub push)
	@test -n "$(FILE)" || { echo "Error: specify FILE=<path>"; exit 1; }
	@grep -qxF "$(FILE)" private_files 2>/dev/null || echo "$(FILE)" >> private_files
	@echo "$(FILE) added to private_files."

# ── Help ───────────────────────────────────────────────────────────────────────

help: ## Show available targets
	@echo ""
	@echo "  The Moment — Makefile targets"
	@echo ""
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' Makefile | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  %-18s %s\n", $$1, $$2}'
	@echo ""

.DEFAULT_GOAL := help
