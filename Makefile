.PHONY: local-up local-down local-logs dev-web dev-web-bg dev-web-stop notif-smoke-test \
	go-test go-integration-test go-lint go-fmt \
	web-test web-e2e web-build web-lint web-format

COMPOSE := docker compose --project-directory . -f docker-compose.yml
API_URL := http://127.0.0.1:8091
NTFY_URL := http://127.0.0.1:8090
SMOKE_APP_ID := smoketest
SMOKE_USER_ID := smoketest-user
SMOKE_TOPIC := $(SMOKE_APP_ID)-alerts

## Local stack (ntfy + provisioning-api)

local-up: ## Bring up the local ntfy + provisioning-api stack
	$(COMPOSE) up -d --build

local-down: ## Tear down the local stack
	$(COMPOSE) down

local-logs: ## Follow logs for the local stack
	$(COMPOSE) logs -f

## Admin UI dev server

dev-web: ## Run the admin UI dev server in the foreground (proxies /v1 to the local API; see web/vite.config.ts)
	cd web && npm run dev -- --host 127.0.0.1

dev-web-bg: ## Start the admin UI dev server detached, logging to /tmp/claude/vite-dev.log
	@mkdir -p /tmp/claude
	@cd web && nohup npm run dev -- --host 127.0.0.1 > /tmp/claude/vite-dev.log 2>&1 & echo $$! > /tmp/claude/vite-dev.pid
	@for i in $$(seq 1 30); do \
		curl -sf http://127.0.0.1:5173/ >/dev/null 2>&1 && break; \
		sleep 1; \
	done
	@curl -sf http://127.0.0.1:5173/ >/dev/null && echo "dev server up at http://127.0.0.1:5173/ (pid $$(cat /tmp/claude/vite-dev.pid))" || { echo "dev server failed to start — see /tmp/claude/vite-dev.log"; exit 1; }

dev-web-stop: ## Stop the detached admin UI dev server started by dev-web-bg
	@if [ -f /tmp/claude/vite-dev.pid ]; then \
		PID=$$(cat /tmp/claude/vite-dev.pid); \
		pkill -P $$PID 2>/dev/null || true; \
		kill $$PID 2>/dev/null || true; \
		rm -f /tmp/claude/vite-dev.pid; \
		echo "dev server stopped"; \
	else echo "no dev server pid file found"; fi

notif-smoke-test: ## Provision a test user, publish a notification, confirm delivery via ntfy's cache, then clean up
	@echo "Waiting for provisioning-api health..."
	@for i in $$(seq 1 30); do \
		curl -sf $(API_URL)/healthz >/dev/null 2>&1 && break; \
		sleep 1; \
	done
	@curl -sf $(API_URL)/healthz >/dev/null || { echo "provisioning-api not healthy at $(API_URL) — run 'make local-up' first"; exit 1; }
	@echo "Provisioning $(SMOKE_USER_ID) into $(SMOKE_APP_ID)..."
	@TOKEN=$$(curl -s -X POST $(API_URL)/v1/provision \
		-H 'Content-Type: application/json' \
		-d '{"app_id":"$(SMOKE_APP_ID)","user_id":"$(SMOKE_USER_ID)"}' | jq -r '.token'); \
	if [ -z "$$TOKEN" ] || [ "$$TOKEN" = "null" ]; then echo "provision failed"; exit 1; fi; \
	echo "Publishing test notification to $(SMOKE_TOPIC)..."; \
	MSG_ID=$$(curl -s -H "Authorization: Bearer $$TOKEN" \
		-d "notif-smoke-test $$(date +%s)" \
		$(NTFY_URL)/$(SMOKE_TOPIC) | jq -r '.id'); \
	if [ -z "$$MSG_ID" ] || [ "$$MSG_ID" = "null" ]; then echo "publish failed"; curl -s -X DELETE $(API_URL)/v1/users/$(SMOKE_USER_ID) >/dev/null; exit 1; fi; \
	echo "Polling ntfy cache for delivery..."; \
	sleep 1; \
	FOUND=$$(curl -s -H "Authorization: Bearer $$TOKEN" \
		"$(NTFY_URL)/$(SMOKE_TOPIC)/json?poll=1&since=all" | jq -r "select(.id == \"$$MSG_ID\") | .id"); \
	curl -s -X DELETE $(API_URL)/v1/users/$(SMOKE_USER_ID) >/dev/null; \
	if [ "$$FOUND" = "$$MSG_ID" ]; then \
		echo "PASS: message $$MSG_ID delivered on $(SMOKE_TOPIC)"; \
	else \
		echo "FAIL: message $$MSG_ID not found in cache"; exit 1; \
	fi

## Go (provisioning-api)

go-test: ## Run Go unit tests
	cd provisioning-api && go test ./...

go-integration-test: ## Run Go integration tests (local stack must be up)
	cd provisioning-api && go test -tags integration ./...

go-lint: ## Check Go formatting and lint
	cd provisioning-api && gofmt -l . && golangci-lint run

go-fmt: ## Apply Go formatting
	cd provisioning-api && gofmt -w .

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
