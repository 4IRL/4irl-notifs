.PHONY: help lint test backup-lint backup-test backup-test-live host-backup host-backup-status local-up local-down local-logs dev-oidc-keys dev-web dev-web-bg dev-web-stop notif-smoke-test \
	go-test go-test-race go-integration-test go-lint go-fmt golangci-pin-check \
	delivery-test delivery-integration-test delivery-integration-test-hold delivery-integration-test-hold-run delivery-integration-test-hold-off delivery-integration-test-hold-off-run delivery-lint delivery-vet-integration delivery-fmt workflows-lint \
	web-test web-e2e web-build web-lint web-format dev-pages \
	setup-web-dev setup-web-test setup-web-e2e setup-web-build setup-web-lint setup-web-format setup-web-pages-dev \
	worker-test worker-build worker-deploy watchdog-test watchdog-build scripts-test scripts-lint scripts-format worktree-ports worktree-new worktree-rm \
	onboard onboard-lint onboard-integration-test credential-audit

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
ifndef DELIVERY_PORT
DELIVERY_PORT := $(or $(call wtenv,DELIVERY_PORT),8300)
endif
ifndef SETUP_WEB_PORT
SETUP_WEB_PORT := $(or $(call wtenv,SETUP_WEB_PORT),5473)
endif
ifndef SETUP_E2E_PORT
SETUP_E2E_PORT := $(or $(call wtenv,SETUP_E2E_PORT),4473)
endif
# Derived, not a scripts/ports.mjs key: tracks DELIVERY_PORT per worktree (local-only test port of delivery-postgres).
DELIVERY_DB_PORT_HOST := $(shell echo $$(($(DELIVERY_PORT) + 10000)))
export COMPOSE_PROJECT_NAME NTFY_PORT API_PORT WEB_PORT E2E_PORT DELIVERY_PORT SETUP_WEB_PORT SETUP_E2E_PORT DELIVERY_DB_PORT_HOST

DEV_DIR := $(CURDIR)/.dev
COMPOSE := docker compose -p $(COMPOSE_PROJECT_NAME) --project-directory . -f docker-compose.yml
API_URL := http://127.0.0.1:$(API_PORT)
DELIVERY_URL := http://127.0.0.1:$(DELIVERY_PORT)
NTFY_URL := http://127.0.0.1:$(NTFY_PORT)

help: ## Show this help message
	@grep -E '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}'

lint: go-lint delivery-lint web-lint setup-web-lint scripts-lint workflows-lint onboard-lint backup-lint ## Run all linters (Go + frontends + scripts + workflows + app onboarding manifests + backup shell)

test: go-test delivery-test web-test setup-web-test worker-test watchdog-test scripts-test backup-test ## Run all unit tests (Go, web + setup-web Vitest, worker Vitest, watchdog Vitest, scripts, backup bash)

# Backup shell scripts (ops/backup, ops/notifs-ops): shellcheck is the exact release in ops/backup/pins.env, fetched
# (sha256-checked, idempotent) into the gitignored .local/bin by ops/backup/fetch-pinned.sh; the system shellcheck is never used.
BACKUP_SH_DIRS := $(wildcard ops/backup ops/notifs-ops)

backup-lint: ## Pinned shellcheck over ops/backup/**/*.sh and ops/notifs-ops/*.sh (passes when a tree has no .sh files)
	@bash ops/backup/fetch-pinned.sh shellcheck --dest .local/bin
	@find $(BACKUP_SH_DIRS) -type f -name '*.sh' -print0 | xargs -0 -r .local/bin/shellcheck -x -P SCRIPTDIR

backup-test: ## Run the offline ops/backup bash tests (every ops/backup/test/test_*.sh except the live ones)
	@bash ops/backup/test/run.sh

backup-test-live: ## Docker round trip: real backup.sh + restore.sh against throwaway Postgres, sqlite and a local restic repo (REQUIRE_DOCKER=1 fails instead of skipping; NBLIVE_SKIP=1 opts out)
	@PATH="$(CURDIR)/.local/bin:$$PATH" bash ops/backup/test/test_backup_live_skip.sh
	@PATH="$(CURDIR)/.local/bin:$$PATH" bash ops/backup/test/test_backup_live.sh

# Host install/verify of the nightly backup. HOST has no default and is only honoured from the command line (a stray HOST
# in the environment never selects a host); the guard runs at parse time, so even `make -n` refuses without it.
BACKUP_HOST_ARGS := install status verify run-now init-repo
ifneq (,$(filter host-backup host-backup-status,$(MAKECMDGOALS)))
ifeq (,$(and $(filter command line,$(origin HOST)),$(strip $(HOST))))
$(error HOST is required: make host-backup HOST=<ssh-alias> ARGS="install|status|verify|run-now|init-repo")
endif
endif
ifneq (,$(filter host-backup,$(MAKECMDGOALS)))
ifneq ($(words $(ARGS)),1)
$(error ARGS must be exactly one of: $(BACKUP_HOST_ARGS))
endif
ifeq (,$(filter $(BACKUP_HOST_ARGS),$(ARGS)))
$(error ARGS must be one of: $(BACKUP_HOST_ARGS))
endif
endif

host-backup: ## Run the backup installer on the VPS with sudo: make host-backup HOST=<ssh-alias> ARGS="install|status|verify|run-now|init-repo"
	@d="$$(ssh $(HOST) mktemp -d)" && trap 'ssh $(HOST) rm -rf "$$d"' EXIT && scp -r ops/backup $(HOST):"$$d"/ && ssh -t $(HOST) sudo bash "$$d"/backup/install-backup.sh $(ARGS) --source "$$d"/backup

host-backup-status: ## Last backup result and timer, no sudo: make host-backup-status HOST=<ssh-alias>
	@ssh $(HOST) 'cat /var/lib/notifs-backup/status/last-run.tsv; systemctl list-timers notifs-backup.timer'

scripts-test: ## Run scripts/ unit tests
	node --test "scripts/*.test.mjs"

scripts-lint: ## Prettier-check scripts/ (uses the Prettier pinned in web/)
	cd web && npx prettier --config .prettierrc --check "../scripts/*.mjs"

scripts-format: ## Auto-format scripts/ with the web/ Prettier config
	cd web && npx prettier --config .prettierrc --write "../scripts/*.mjs"

worktree-ports: ## Print this checkout's resolved ports
	@node scripts/ports.mjs print

# `env -u` drops the exported primary ports so the resolver picks fresh ones (target-specific `unexport` fails to parse on make 4.3).
worktree-new: ## Create a worktree: make worktree-new name=<slug> [b=<branch>] [base=<ref>]
	@env -u NTFY_PORT -u API_PORT -u WEB_PORT -u E2E_PORT -u DELIVERY_PORT -u SETUP_WEB_PORT -u SETUP_E2E_PORT WT_NAME='$(subst ','\'',$(name))' WT_BRANCH='$(subst ','\'',$(b))' WT_BASE='$(subst ','\'',$(base))' node scripts/worktree.mjs new

worktree-rm: ## Remove this worktree (run inside it)
	@node scripts/worktree.mjs rm

## Local stack (ntfy + person-service + provisioning-api + delivery-api + Postgres)

# dev-oidc-keys first: docker-compose.yml bind-mounts .local/dev-oidc/jwks.json into delivery-api, and
# a missing mount source would be created as an empty directory instead of failing.
local-up: dev-oidc-keys ## Bring up the local ntfy + person-service + provisioning-api + delivery-api (+ Postgres, migrate) stack
	$(COMPOSE) up -d --build

dev-oidc-keys: ## Generate the throwaway dev/CI OIDC signing key pair under .local/dev-oidc/ (gitignored; idempotent)
	node scripts/dev-oidc-keys.mjs

local-down: ## Tear down the local stack
	$(COMPOSE) down

local-logs: ## Follow logs for the local stack
	$(COMPOSE) logs -f

## Admin UI dev server

dev-web: ## Run the admin UI dev server in the foreground (proxies /delivery to the local delivery-api; see web/vite.config.ts)
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

notif-smoke-test: ## Provision a test subscriber, deliver a to.recipient message and confirm subscriber delivery, all through delivery-api, then clean up
	@NOTIFS_DELIVERY_URL=$(DELIVERY_URL) NOTIFS_NTFY_URL=$(NTFY_URL) node scripts/notif-smoke-test.mjs

## Go (provisioning-api)

# golangci-lint is pinned exactly and run through `go run`, like actionlint below, so a local `make
# go-lint` / `make delivery-lint` runs the same version as CI instead of whatever is on PATH (a newer local
# version once passed code that CI's v2.12.2 `unparam` rejected). Keep it equal to the `version:` of both
# golangci-lint steps in .github/workflows/lint.yml; golangci-pin-check fails the lint targets on drift.
GOLANGCI_LINT_VERSION := v2.12.2
GOLANGCI_LINT := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

golangci-pin-check: ## Check the Makefile's golangci-lint version matches .github/workflows/lint.yml
	@total=$$(grep -c '^ *version: v' .github/workflows/lint.yml); \
	matching=$$(grep -c '^ *version: $(GOLANGCI_LINT_VERSION)$$' .github/workflows/lint.yml); \
	if [ "$$total" -eq 0 ] || [ "$$total" -ne "$$matching" ]; then \
		echo "golangci-lint version drift: Makefile pins $(GOLANGCI_LINT_VERSION), .github/workflows/lint.yml has $$matching of $$total matching"; exit 1; \
	fi

go-test: ## Run Go unit tests
	cd provisioning-api && go test ./...

go-test-race: ## Run Go unit tests under the race detector
	cd provisioning-api && go test -race ./...

go-integration-test: ## Run Go integration tests (local stack must be up)
	@$(INTERNAL_CREDENTIAL_LOOKUP); C="$$($(COMPOSE) ps -q provisioning-api)"; cd provisioning-api && NOTIFS_INTERNAL_CREDENTIAL="$$CRED" NOTIFS_API_CONTAINER="$$C" NOTIFS_API_URL=$(API_URL) NTFY_URL=$(NTFY_URL) go test -tags integration ./...

go-lint: golangci-pin-check ## Check Go formatting and lint
	@UNFORMATTED="$$(cd provisioning-api && gofmt -l .)"; test -z "$$UNFORMATTED" || { echo "Files need gofmt:"; echo "$$UNFORMATTED"; exit 1; }
	cd provisioning-api && $(GOLANGCI_LINT) run

go-fmt: ## Apply Go formatting
	cd provisioning-api && gofmt -w .

## Go (delivery-api)

delivery-test: ## Run delivery-api Go unit tests
	cd delivery-api && go test ./...

# -p 1 runs the integration packages one at a time: they share one database, and
# pgstore_integration_test.go briefly writes a failure row that /readyz would report.
# NOTIFS_DELIVERY_MAX_SCHEDULED forwards the stack's per-app scheduled cap (set the same
# DELIVERY_MAX_SCHEDULED_PER_APP for `make local-up` and this target) so the live 429 test
# can fill a small cap; it skips when the cap is not small.
delivery-integration-test: ## Run delivery-api integration tests (local stack must be up)
	@C="$$($(COMPOSE) ps -q delivery-api)"; D="$$($(COMPOSE) ps -q delivery-postgres)"; cd delivery-api && NOTIFS_DELIVERY_CONTAINER="$$C" NOTIFS_DELIVERY_DB_CONTAINER="$$D" NOTIFS_DELIVERY_MAX_SCHEDULED="$${DELIVERY_MAX_SCHEDULED_PER_APP:-1000}" NOTIFS_DELIVERY_HOLD_UNROUTED="$${DELIVERY_HOLD_UNROUTED:-$(DELIVERY_HOLD_DEFAULT)}" NOTIFS_DELIVERY_MAX_HELD="$${DELIVERY_MAX_HELD_PER_APP:-1000}" NOTIFS_DELIVERY_URL=$(DELIVERY_URL) NOTIFS_NTFY_URL=$(NTFY_URL) NOTIFS_DELIVERY_TEST_DSN="postgres://delivery:delivery-local-dev@127.0.0.1:$(DELIVERY_DB_PORT_HOST)/delivery?sslmode=disable" go test -p 1 -tags integration ./...

# Live hold tests (delivery-api/integration/held_integration_test.go, names start with TestHold) and the
# onboard verify unrouted case need a stack with DELIVERY_HOLD_UNROUTED on (the default) and, for the two
# cap tests, a small DELIVERY_MAX_HELD_PER_APP / DELIVERY_MAX_HELD_TOTAL. A plain
# `make delivery-integration-test` / `make onboard-integration-test` runs on the default (flag-on) stack,
# so it runs the hold tests and skips only the cap tests; the 422 assertions live in the explicit
# flag-off pass (`make delivery-integration-test-hold-off`).
#
# The -run target only RUNS the tests against an already flag-on stack and never touches the stack (CI
# recreates the stack with its own compose command, then calls this). NOTIFS_DELIVERY_MAX_HELD must equal
# the stack's DELIVERY_MAX_HELD_PER_APP and NOTIFS_DELIVERY_MAX_HELD_TOTAL its DELIVERY_MAX_HELD_TOTAL. Both runners always execute; the exit status is the first
# failure. A runner that ran zero hold tests fails the target too: Go needs a `--- PASS: TestHold` line,
# node an `ok` line for the unrouted case without `# SKIP`. NOTIFS_DELIVERY_DB_CONTAINER may be exported
# to point the Go tests at a stack this Makefile's compose project does not own (CI).
delivery-integration-test-hold-run: ## Run the live hold tests (Go + onboard) against an already flag-on stack (never touches the stack)
	@$(INTERNAL_CREDENTIAL_LOOKUP); \
	OUT="$$(mktemp -d)" || exit 1; \
	C="$$($(COMPOSE) ps -q delivery-api)"; D="$${NOTIFS_DELIVERY_DB_CONTAINER:-$$($(COMPOSE) ps -q delivery-postgres)}"; \
	GO_RC=0; NODE_RC=0; \
	(cd delivery-api && NOTIFS_DELIVERY_HOLD_UNROUTED=1 NOTIFS_DELIVERY_MAX_HELD="$${NOTIFS_DELIVERY_MAX_HELD:-}" NOTIFS_DELIVERY_MAX_HELD_TOTAL="$${NOTIFS_DELIVERY_MAX_HELD_TOTAL:-}" NOTIFS_DELIVERY_CONTAINER="$$C" NOTIFS_DELIVERY_DB_CONTAINER="$$D" NOTIFS_DELIVERY_URL=$(DELIVERY_URL) NOTIFS_NTFY_URL=$(NTFY_URL) go test -v -count=1 -p 1 -tags integration -run '^TestHold' ./integration/) > "$$OUT/go.txt" 2>&1 || GO_RC=$$?; \
	cat "$$OUT/go.txt"; \
	NOTIFS_DELIVERY_HOLD_UNROUTED=1 ONBOARD_INTEGRATION_URL=$(DELIVERY_URL) ONBOARD_INTEGRATION_PROVISIONING_URL=$(API_URL) NOTIFS_INTERNAL_CREDENTIAL="$$CRED" node --test --test-reporter=tap scripts/onboard.integration.test.mjs > "$$OUT/node.txt" 2>&1 || NODE_RC=$$?; \
	cat "$$OUT/node.txt"; \
	GO_PASSED=0; grep -Eq '^[[:space:]]*--- PASS: TestHold' "$$OUT/go.txt" && GO_PASSED=1; \
	CAPS_PASSED=0; grep -Eq '^[[:space:]]*--- PASS: TestHoldPerAppCapIs429' "$$OUT/go.txt" && grep -Eq '^[[:space:]]*--- PASS: TestHoldTotalCapIs429' "$$OUT/go.txt" && CAPS_PASSED=1; \
	NODE_PASSED=0; grep -E '^[[:space:]]*ok [0-9]+ - reports an unrouted channel' "$$OUT/node.txt" | grep -qv '# SKIP' && NODE_PASSED=1; \
	rm -rf "$$OUT"; \
	if [ "$$GO_RC" -ne 0 ]; then echo "hold: the Go hold tests failed (exit $$GO_RC)" >&2; exit "$$GO_RC"; fi; \
	if [ "$$NODE_RC" -ne 0 ]; then echo "hold: the onboard integration test failed (exit $$NODE_RC)" >&2; exit "$$NODE_RC"; fi; \
	if [ "$$GO_PASSED" -ne 1 ]; then echo "hold: zero Go hold tests passed (no '--- PASS: TestHold' line): NOTIFS_DELIVERY_HOLD_UNROUTED was not forwarded to go test, or every test skipped" >&2; exit 1; fi; \
	if [ "$$CAPS_PASSED" -ne 1 ]; then echo "hold: TestHoldPerAppCapIs429 and TestHoldTotalCapIs429 must both pass (a skip counts as failure): NOTIFS_DELIVERY_MAX_HELD and NOTIFS_DELIVERY_MAX_HELD_TOTAL must be set to the stack's small caps" >&2; exit 1; fi; \
	if [ "$$NODE_PASSED" -ne 1 ]; then echo "hold: the onboard unrouted test did not pass (skipped or missing): check ONBOARD_INTEGRATION_URL, ONBOARD_INTEGRATION_PROVISIONING_URL, NOTIFS_INTERNAL_CREDENTIAL and NOTIFS_DELIVERY_HOLD_UNROUTED" >&2; exit 1; fi

# Local composed target (`up -d --build` re-applies the environment, rebuilds the image from this checkout
# and re-runs delivery-migrate when needed): recreate delivery-api with the hold flag on and a small per-app cap
# (HOLD_TEST_MAX_HELD, default 3), run the runners target, then recreate it on the default hold flag. The
# restore always runs (even when the tests fail), waits for delivery-api to be healthy and checks the new
# container's environment says DELIVERY_HOLD_UNROUTED=$(DELIVERY_HOLD_DEFAULT). The exit status is the
# test failure; a failed restore only fails the target when the tests passed. Other DELIVERY_* settings in the environment (for example
# DELIVERY_MAX_SCHEDULED_PER_APP) carry over to both recreates.
HOLD_TEST_MAX_HELD ?= 3
HOLD_TEST_MAX_HELD_TOTAL ?= 20
HOLD_WAIT_HEALTHY = wait_healthy() { for _ in $$(seq 1 60); do STATUS="$$(docker inspect -f '{{.State.Health.Status}}' "$$($(COMPOSE) ps -q delivery-api)" 2>/dev/null || echo unknown)"; if [ "$$STATUS" = healthy ]; then return 0; fi; sleep 2; done; echo "delivery-api did not become healthy in time" >&2; return 1; }

# DELIVERY_HOLD_DEFAULT is the hold flag value the local stack runs with outside the hold targets: the
# restore fragment recreates with it, and the plain test target forwards it. It must equal the code default
# and both compose files' integer default (a unit test in delivery-api/internal/message enforces it).
DELIVERY_HOLD_DEFAULT := 1
# Shared by the hold and hold-off targets (they define wait_healthy and RESTORE_RC first): recreate
# delivery-api with the default flag, wait healthy, assert that exact line in the container env.
HOLD_RESTORE = restore() { echo "hold: restoring delivery-api with DELIVERY_HOLD_UNROUTED=$(DELIVERY_HOLD_DEFAULT)"; \
	DELIVERY_HOLD_UNROUTED=$(DELIVERY_HOLD_DEFAULT) $(COMPOSE) up -d --build delivery-api || RESTORE_RC=$$?; \
	if [ "$$RESTORE_RC" -eq 0 ]; then wait_healthy || RESTORE_RC=$$?; fi; \
	if [ "$$RESTORE_RC" -eq 0 ]; then docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$$($(COMPOSE) ps -q delivery-api)" | grep -qx 'DELIVERY_HOLD_UNROUTED=$(DELIVERY_HOLD_DEFAULT)' || { echo "hold: the restored delivery-api does not have DELIVERY_HOLD_UNROUTED=$(DELIVERY_HOLD_DEFAULT)" >&2; RESTORE_RC=1; }; fi; \
	if [ "$$RESTORE_RC" -ne 0 ]; then echo "hold: RESTORE FAILED: delivery-api may not be back on the default hold setting; run 'make local-up'" >&2; fi; }

# Flag-off pass (the third pass beside plain and hold): the TestUnroutedSubmitIsRefusedWhenHoldOff and
# TestAnotherAppsDestinationIsRefusedWhenHoldOff tests pin the 422 and run only on a stack with the flag
# explicitly 0. The -run target only runs them against an already flag-off stack (CI calls it after its own
# recreate); both tests must PASS (a skip fails the target).
delivery-integration-test-hold-off-run: ## Run the flag-off live tests against an already flag-off stack (never touches the stack)
	@OUT="$$(mktemp -d)" || exit 1; \
	C="$$($(COMPOSE) ps -q delivery-api)"; D="$${NOTIFS_DELIVERY_DB_CONTAINER:-$$($(COMPOSE) ps -q delivery-postgres)}"; \
	GO_RC=0; \
	(cd delivery-api && NOTIFS_DELIVERY_HOLD_UNROUTED=0 NOTIFS_DELIVERY_CONTAINER="$$C" NOTIFS_DELIVERY_DB_CONTAINER="$$D" NOTIFS_DELIVERY_URL=$(DELIVERY_URL) NOTIFS_NTFY_URL=$(NTFY_URL) NOTIFS_DELIVERY_TEST_DSN="postgres://delivery:delivery-local-dev@127.0.0.1:$(DELIVERY_DB_PORT_HOST)/delivery?sslmode=disable" go test -v -count=1 -p 1 -tags integration -run 'WhenHoldOff$$' ./integration/) > "$$OUT/go.txt" 2>&1 || GO_RC=$$?; \
	cat "$$OUT/go.txt"; \
	BOTH_PASSED=0; grep -Eq '^[[:space:]]*--- PASS: TestUnroutedSubmitIsRefusedWhenHoldOff' "$$OUT/go.txt" && grep -Eq '^[[:space:]]*--- PASS: TestAnotherAppsDestinationIsRefusedWhenHoldOff' "$$OUT/go.txt" && BOTH_PASSED=1; \
	rm -rf "$$OUT"; \
	if [ "$$GO_RC" -ne 0 ]; then echo "hold-off: the flag-off tests failed (exit $$GO_RC)" >&2; exit "$$GO_RC"; fi; \
	if [ "$$BOTH_PASSED" -ne 1 ]; then echo "hold-off: TestUnroutedSubmitIsRefusedWhenHoldOff and TestAnotherAppsDestinationIsRefusedWhenHoldOff must both pass (a skip counts as failure): NOTIFS_DELIVERY_HOLD_UNROUTED must be exactly 0" >&2; exit 1; fi

delivery-integration-test-hold-off: ## Recreate delivery-api with the hold flag explicitly off, run the flag-off live tests, restore the default (local stack must be up)
	@$(HOLD_WAIT_HEALTHY); \
	RC=0; RESTORE_RC=0; \
	$(HOLD_RESTORE); \
	trap 'restore; exit 130' INT TERM; \
	wait_healthy || RC=$$?; \
	if [ "$$RC" -eq 0 ]; then echo "hold-off: recreating delivery-api with DELIVERY_HOLD_UNROUTED=0"; DELIVERY_HOLD_UNROUTED=0 $(COMPOSE) up -d --build delivery-api || RC=$$?; fi; \
	if [ "$$RC" -eq 0 ]; then wait_healthy || RC=$$?; fi; \
	D="$${NOTIFS_DELIVERY_DB_CONTAINER:-$$($(COMPOSE) ps -q delivery-postgres)}"; \
	if [ "$$RC" -eq 0 ]; then NOTIFS_DELIVERY_DB_CONTAINER="$$D" $(MAKE) delivery-integration-test-hold-off-run || RC=$$?; fi; \
	trap - INT TERM; restore; \
	if [ "$$RC" -ne 0 ]; then exit "$$RC"; fi; \
	exit "$$RESTORE_RC"

delivery-integration-test-hold: ## Recreate delivery-api flag-on, run the live hold tests, restore the default (local stack must be up)
	@$(HOLD_WAIT_HEALTHY); \
	RC=0; RESTORE_RC=0; \
	$(HOLD_RESTORE); \
	trap 'restore; exit 130' INT TERM; \
	echo "hold: recreating delivery-api with DELIVERY_HOLD_UNROUTED=1 DELIVERY_MAX_HELD_PER_APP=$(HOLD_TEST_MAX_HELD)"; \
	DELIVERY_HOLD_UNROUTED=1 DELIVERY_MAX_HELD_PER_APP=$(HOLD_TEST_MAX_HELD) DELIVERY_MAX_HELD_TOTAL=$(HOLD_TEST_MAX_HELD_TOTAL) $(COMPOSE) up -d --build delivery-api || RC=$$?; \
	if [ "$$RC" -eq 0 ]; then wait_healthy || RC=$$?; fi; \
	if [ "$$RC" -eq 0 ]; then NOTIFS_DELIVERY_MAX_HELD=$(HOLD_TEST_MAX_HELD) NOTIFS_DELIVERY_MAX_HELD_TOTAL=$(HOLD_TEST_MAX_HELD_TOTAL) $(MAKE) delivery-integration-test-hold-run || RC=$$?; fi; \
	trap - INT TERM; restore; \
	if [ "$$RC" -ne 0 ]; then exit "$$RC"; fi; \
	exit "$$RESTORE_RC"

delivery-lint: golangci-pin-check ## Check delivery-api Go formatting and lint
	@UNFORMATTED="$$(cd delivery-api && gofmt -l .)"; test -z "$$UNFORMATTED" || { echo "Files need gofmt:"; echo "$$UNFORMATTED"; exit 1; }
	cd delivery-api && $(GOLANGCI_LINT) run

delivery-vet-integration: ## Vet delivery-api including integration-tagged files
	cd delivery-api && go vet -tags integration ./...

delivery-fmt: ## Apply delivery-api Go formatting
	cd delivery-api && gofmt -w .

## Workflows (GitHub Actions)

# Pinned exactly; `go run` fetches the module on first use, so it stays out of both go.mod files.
# actionlint also runs shellcheck on each `run:` block when shellcheck is on PATH.
# actionlint only understands workflow files (given an action.yml it just reports a missing "jobs"
# section), so composite actions under .github/actions/ are linted through generated one-job wrapper
# workflows in the gitignored .local/action-lint/ (see scripts/composite-action-lint.mjs). The node
# script prints the wrapper paths it wrote (none when there is no composite action) and runs first;
# its non-zero exit (a malformed action, a run step without shell:) fails the target before actionlint.
workflows-lint: ## Lint GitHub Actions workflows and composite actions with actionlint
	@wrappers=$$(node scripts/composite-action-lint.mjs) || exit 1; \
	go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 ./.github/workflows/*.yml $$wrappers

## Web (admin UI)

web-test: ## Run frontend unit tests (Vitest)
	cd web && npm test

web-e2e: ## Run frontend e2e tests (Playwright); E2E_ARGS=... passes extra args
	cd web && npx playwright test $(E2E_ARGS)

web-build: ## Production build (tsc -b + Vite)
	cd web && npm run build

web-lint: ## Lint and format-check the frontend
	cd web && npx eslint . && npx prettier --check .

web-format: ## Auto-format the frontend
	cd web && npx prettier --write .

dev-pages: ## Build + serve the admin UI via wrangler pages dev to exercise the Pages Functions proxies locally (copy web/.dev.vars.example -> web/.dev.vars first)
	cd web && npm run pages-dev

## Setup page (setup-web: public static ntfy setup page, no API calls)

setup-web-dev: ## Run the setup page dev server in the foreground on SETUP_WEB_PORT
	cd setup-web && npm run dev -- --host 127.0.0.1 --port $(SETUP_WEB_PORT) --strictPort

setup-web-test: ## Run setup page unit tests (Vitest)
	cd setup-web && npm test

setup-web-e2e: ## Run setup page e2e tests (Playwright)
	cd setup-web && npx playwright test

setup-web-build: ## Production build of the setup page (tsc -b + Vite)
	cd setup-web && npm run build

setup-web-lint: ## Lint and format-check the setup page
	cd setup-web && npx eslint . && npx prettier --check .

setup-web-format: ## Auto-format the setup page
	cd setup-web && npx prettier --write .

setup-web-pages-dev: ## Build + serve the setup page via wrangler pages dev for local Pages parity (headers, routing)
	cd setup-web && npm run pages-dev

## Worker (person-service)

worker-test: ## Run person-service unit/integration tests (Vitest + workers pool)
	cd person-service && npm test

worker-build: ## Typecheck and dry-run bundle person-service (tsc --noEmit + wrangler deploy --dry-run)
	cd person-service && npm run build

worker-deploy: ## Deploy person-service to Cloudflare (operator-run post-merge; needs wrangler auth)
	cd person-service && npx wrangler deploy

## Worker (watchdog)

watchdog-test: ## Run watchdog Worker tests (Vitest + workers pool); filter with args="-t <name>"
	cd watchdog && npm test -- $(args)

watchdog-build: ## Typecheck and dry-run bundle the watchdog Worker (tsc --noEmit + wrangler deploy --dry-run)
	cd watchdog && npm run build

## App onboarding (scripts/onboard.mjs)

# app/cmd/env/prune/resend_secrets are quoted with the worktree-new $(subst ...) idiom (escape embedded
# single quotes, wrap in single quotes) so each one reaches node as one literal argument/value, never
# interpolated into a shell string — see docs/app-integration-guide.md "Onboarding with a manifest".
onboard: ## Plan/apply/verify app onboarding: make onboard app=<app_id|_platform> [cmd=plan|apply|verify] [env=<name>] [prune=1] [resend_secrets=1]
	@$(if $(findstring /,$(app)),$(error app must be a bare app_id or _platform, not a path))
	@ONBOARD_URL="$${ONBOARD_URL:-$(DELIVERY_URL)}" node scripts/onboard.mjs '$(subst ','\'',$(or $(cmd),plan))' 'apps/$(subst ','\'',$(app)).json' $(if $(env),--env '$(subst ','\'',$(env))') $(if $(filter 1,$(prune)),--prune) $(if $(filter 1,$(resend_secrets)),--resend-secrets)

onboard-lint: ## Offline lint of every manifest under apps/ and the notifs_ops manifest (no network call; part of `make lint`)
	node scripts/onboard.mjs lint apps
	node scripts/onboard.mjs lint --manifest ops/notifs-ops/manifest.json --target app

# Shared shell lookup of the harness credential (scripts/internal-credential.sh, unit-tested by
# internal-credential.test.mjs): an already-set NOTIFS_INTERNAL_CREDENTIAL wins as is; otherwise the
# script reads PROVISIONING_INTERNAL_CREDENTIAL from the running provisioning-api container. Empty
# either way fails closed. Never echoes the value.
INTERNAL_CREDENTIAL_LOOKUP = CRED="$$(COMPOSE='$(COMPOSE)' sh scripts/internal-credential.sh)" || exit 1

credential-audit: SHELL := /bin/bash
credential-audit: ## Fail if provisioning-api logged any request the internal-credential check would reject (SINCE=<docker --since value> to scope)
	@set -o pipefail; $(COMPOSE) logs --no-color $(if $(SINCE),--since '$(SINCE)') provisioning-api | node scripts/credential-audit.mjs

# Needs the local stack, like delivery-integration-test. Sets ONBOARD_INTEGRATION_URL so
# scripts/onboard.integration.test.mjs runs instead of skipping; `make scripts-test` (no env set)
# stays skipped-green with no stack. ONBOARD_INTEGRATION_PROVISIONING_URL lets the test mint (and
# revoke) a real ntfy publisher token via provisioning-api, so its owned ntfy destination actually
# authenticates against ntfy's auth-default-access: deny-all; that one direct call carries
# NOTIFS_INTERNAL_CREDENTIAL (looked up below), and the test's teardown goes through the door's
# operator deprovision instead of provisioning-api. Runs only that one file, not the whole scripts/
# glob.
onboard-integration-test: ## Run the live app onboarding integration test (local stack must be up)
	@$(INTERNAL_CREDENTIAL_LOOKUP); ONBOARD_INTEGRATION_URL=$(DELIVERY_URL) ONBOARD_INTEGRATION_PROVISIONING_URL=$(API_URL) NOTIFS_INTERNAL_CREDENTIAL="$$CRED" node --test scripts/onboard.integration.test.mjs
