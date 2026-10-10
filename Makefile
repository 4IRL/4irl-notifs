.PHONY: help lint test local-up local-down local-logs dev-web dev-web-bg dev-web-stop notif-smoke-test \
	go-test go-integration-test go-lint go-fmt \
	delivery-test delivery-lint delivery-fmt \
	web-test web-e2e web-build web-lint web-format dev-pages \
	worker-test worker-build worker-deploy scripts-test scripts-lint scripts-format worktree-ports worktree-new worktree-rm

.DEFAULT_GOAL := help

# Per-worktree identity and ports. Precedence: defaults < .worktree.env < environment < `make VAR=...`.
# Single keys are read from .worktree.env (plain KEY=VALUE) instead of `-include`, so an
# environment value is never overridden by the file. Must stay above the `:=` lines below.
wtenv = $(shell sed -n 's/^$(1)=//p' .worktree.env 2>/dev/null | tail -n 1)

ifndef COMPOSE_PROJECT_NAME
COMPOSE_PROJECT_NAME := $(or $(call wtenv,COMPOSE_PROJECT_NAME),4irl-notifs)
endif
ifndef NTFY_PORT
NTFY_PORT := $(or $(call wtenv,NTFY_PORT),8090)
endif
ifndef API_PORT
API_PORT := $(or $(call wtenv,API_PORT),8091)
endif
ifndef WEB_PORT
WEB_PORT := $(or $(call wtenv,WEB_PORT),5173)
endif
ifndef E2E_PORT
E2E_PORT := $(or $(call wtenv,E2E_PORT),4173)
endif
export COMPOSE_PROJECT_NAME NTFY_PORT API_PORT WEB_PORT E2E_PORT

DEV_DIR := $(CURDIR)/.dev
COMPOSE := docker compose -p $(COMPOSE_PROJECT_NAME) --project-directory . -f docker-compose.yml
API_URL := http://127.0.0.1:$(API_PORT)
NTFY_URL := http://127.0.0.1:$(NTFY_PORT)
SMOKE_APP_ID := smoketest
SMOKE_EMAIL := smoketest@example.com

help: ## Show this help message
	@grep -E '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}'

lint: go-lint delivery-lint web-lint scripts-lint ## Run all linters (Go + frontend + scripts)

test: go-test delivery-test web-test worker-test scripts-test ## Run all unit tests (Go, web Vitest, worker Vitest, scripts)

scripts-test: ## Run scripts/ unit tests
	node --test "scripts/*.test.mjs"

scripts-lint: ## Prettier-check scripts/ (uses the Prettier pinned in web/)
	cd web && npx prettier --config .prettierrc --check "../scripts/*.mjs"

scripts-format: ## Auto-format scripts/ with the web/ Prettier config
	cd web && npx prettier --config .prettierrc --write "../scripts/*.mjs"

worktree-ports: ## Print this checkout's resolved ports
	@node scripts/ports.mjs print

# The export line above would hand the primary's default ports to the resolver; drop them for this recipe.
worktree-new: unexport NTFY_PORT := $(NTFY_PORT)
worktree-new: unexport API_PORT := $(API_PORT)
worktree-new: unexport WEB_PORT := $(WEB_PORT)
worktree-new: unexport E2E_PORT := $(E2E_PORT)
worktree-new: ## Create a worktree: make worktree-new name=<slug> [b=<branch>] [base=<ref>]
	@WT_NAME='$(subst ','\'',$(name))' WT_BRANCH='$(subst ','\'',$(b))' WT_BASE='$(subst ','\'',$(base))' node scripts/worktree.mjs new

worktree-rm: ## Remove this worktree (run inside it)
	@node scripts/worktree.mjs rm

## Local stack (ntfy + provisioning-api)

local-up: ## Bring up the local ntfy + provisioning-api stack
	$(COMPOSE) up -d --build

local-down: ## Tear down the local stack
	$(COMPOSE) down

local-logs: ## Follow logs for the local stack
	$(COMPOSE) logs -f

## Admin UI dev server

dev-web: ## Run the admin UI dev server in the foreground (proxies /v1 to the local API; see web/vite.config.ts)
	cd web && npm run dev -- --host 127.0.0.1 --port $(WEB_PORT) --strictPort

dev-web-bg: ## Start the admin UI dev server detached, logging to .dev/vite-dev.log
	@mkdir -p $(DEV_DIR)
	@cd web && nohup npm run dev -- --host 127.0.0.1 --port $(WEB_PORT) --strictPort > $(DEV_DIR)/vite-dev.log 2>&1 & echo $$! > $(DEV_DIR)/vite-dev.pid
	@for i in $$(seq 1 30); do \
		curl -sf http://127.0.0.1:$(WEB_PORT)/ >/dev/null 2>&1 && break; \
		sleep 1; \
	done
	@curl -sf http://127.0.0.1:$(WEB_PORT)/ >/dev/null && echo "dev server up at http://127.0.0.1:$(WEB_PORT)/ (pid $$(cat $(DEV_DIR)/vite-dev.pid))" || { echo "dev server failed to start — see $(DEV_DIR)/vite-dev.log"; exit 1; }

dev-web-stop: ## Stop the detached admin UI dev server started by dev-web-bg
	@if [ -f $(DEV_DIR)/vite-dev.pid ]; then \
		PID=$$(cat $(DEV_DIR)/vite-dev.pid); \
		kill_tree() { for CHILD in $$(pgrep -P $$1); do kill_tree $$CHILD; done; kill $$1 2>/dev/null || true; }; \
		kill_tree $$PID; \
		rm -f $(DEV_DIR)/vite-dev.pid; \
		echo "dev server stopped"; \
	else echo "no dev server pid file found"; fi

notif-smoke-test: ## Provision an app publisher and a test user, publish as the publisher, confirm subscriber delivery via ntfy's cache, then clean up
	@echo "Waiting for provisioning-api health..."
	@for i in $$(seq 1 30); do \
		curl -sf $(API_URL)/healthz >/dev/null 2>&1 && break; \
		sleep 1; \
	done
	@curl -sf $(API_URL)/healthz >/dev/null || { echo "provisioning-api not healthy at $(API_URL) — run 'make local-up' first"; exit 1; }
	@echo "Provisioning publisher for $(SMOKE_APP_ID)..."
	@PUB_TOKEN=$$(curl -s -X POST $(API_URL)/v1/provision-app \
		-H 'Content-Type: application/json' \
		-d '{"app_id":"$(SMOKE_APP_ID)"}' | jq -r '.token'); \
	if [ -z "$$PUB_TOKEN" ] || [ "$$PUB_TOKEN" = "null" ]; then echo "provision-app failed"; exit 1; fi; \
	echo "Provisioning $(SMOKE_EMAIL) into $(SMOKE_APP_ID)..."; \
	PROVISION_RESPONSE=$$(curl -s -X POST $(API_URL)/v1/provision \
		-H 'Content-Type: application/json' \
		-d '{"app_id":"$(SMOKE_APP_ID)","email":"$(SMOKE_EMAIL)"}'); \
	TOKEN=$$(echo "$$PROVISION_RESPONSE" | jq -r '.token'); \
	HASH=$$(echo "$$PROVISION_RESPONSE" | jq -r '.person_hash'); \
	NTFY_USER=$$(echo "$$PROVISION_RESPONSE" | jq -r '.user_id'); \
	if [ -z "$$TOKEN" ] || [ "$$TOKEN" = "null" ]; then \
		echo "provision failed"; \
		curl -s -X DELETE $(API_URL)/v1/users/$(SMOKE_APP_ID)-publisher >/dev/null; \
		exit 1; \
	fi; \
	SMOKE_TOPIC="$(SMOKE_APP_ID)-$$HASH-alerts"; \
	echo "Publishing test notification to $$SMOKE_TOPIC as publisher..."; \
	MSG_ID=$$(curl -s -H "Authorization: Bearer $$PUB_TOKEN" \
		-d "notif-smoke-test $$(date +%s)" \
		$(NTFY_URL)/$$SMOKE_TOPIC | jq -r '.id'); \
	if [ -z "$$MSG_ID" ] || [ "$$MSG_ID" = "null" ]; then \
		echo "publish failed"; \
		curl -s -X DELETE $(API_URL)/v1/users/$$NTFY_USER >/dev/null; \
		curl -s -X DELETE $(API_URL)/v1/users/$(SMOKE_APP_ID)-publisher >/dev/null; \
		exit 1; \
	fi; \
	echo "Polling ntfy cache for delivery..."; \
	sleep 1; \
	FOUND=$$(curl -s -H "Authorization: Bearer $$TOKEN" \
		"$(NTFY_URL)/$$SMOKE_TOPIC/json?poll=1&since=all" | jq -r "select(.id == \"$$MSG_ID\") | .id"); \
	curl -s -X DELETE $(API_URL)/v1/users/$$NTFY_USER >/dev/null; \
	curl -s -X DELETE $(API_URL)/v1/users/$(SMOKE_APP_ID)-publisher >/dev/null; \
	if [ "$$FOUND" = "$$MSG_ID" ]; then \
		echo "PASS: message $$MSG_ID delivered on $$SMOKE_TOPIC"; \
	else \
		echo "FAIL: message $$MSG_ID not found in cache"; exit 1; \
	fi

## Go (provisioning-api)

go-test: ## Run Go unit tests
	cd provisioning-api && go test ./...

go-integration-test: ## Run Go integration tests (local stack must be up)
	@C="$$($(COMPOSE) ps -q provisioning-api)"; cd provisioning-api && NOTIFS_API_CONTAINER="$$C" NOTIFS_API_URL=$(API_URL) NTFY_URL=$(NTFY_URL) go test -tags integration ./...

go-lint: ## Check Go formatting and lint
	@UNFORMATTED="$$(cd provisioning-api && gofmt -l .)"; test -z "$$UNFORMATTED" || { echo "Files need gofmt:"; echo "$$UNFORMATTED"; exit 1; }
	cd provisioning-api && golangci-lint run

go-fmt: ## Apply Go formatting
	cd provisioning-api && gofmt -w .

## Go (delivery-api)

delivery-test: ## Run delivery-api Go unit tests
	cd delivery-api && go test ./...

delivery-lint: ## Check delivery-api Go formatting and lint
	@UNFORMATTED="$$(cd delivery-api && gofmt -l .)"; test -z "$$UNFORMATTED" || { echo "Files need gofmt:"; echo "$$UNFORMATTED"; exit 1; }
	cd delivery-api && golangci-lint run

delivery-fmt: ## Apply delivery-api Go formatting
	cd delivery-api && gofmt -w .

## Web (admin UI)

web-test: ## Run frontend unit tests (Vitest)
	cd web && npm test

web-e2e: ## Run frontend e2e tests (Playwright)
	cd web && npx playwright test

web-build: ## Production build (tsc -b + Vite)
	cd web && npm run build

web-lint: ## Lint and format-check the frontend
	cd web && npx eslint . && npx prettier --check .

web-format: ## Auto-format the frontend
	cd web && npx prettier --write .

dev-pages: ## Build + serve the admin UI via wrangler pages dev to exercise the Pages Functions proxies locally (copy web/.dev.vars.example -> web/.dev.vars first)
	cd web && npm run pages-dev

## Worker (person-service)

worker-test: ## Run person-service unit/integration tests (Vitest + workers pool)
	cd person-service && npm test

worker-build: ## Typecheck and dry-run bundle person-service (tsc --noEmit + wrangler deploy --dry-run)
	cd person-service && npm run build

worker-deploy: ## Deploy person-service to Cloudflare (operator-run post-merge; needs wrangler auth)
	cd person-service && npx wrangler deploy
