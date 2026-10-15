# Alpine dev tasks. Requires Docker, Go 1.26+ (auto toolchain), Node 22 + pnpm.
SHELL := /bin/bash
-include .env
# Live-mode overrides (cp .env.live.example .env.live); git-ignored.
-include .env.live
export

GO_MODULES := packages/shared packages/agents packages/gates services/api services/integrations services/orchestrator services/runner
COMPOSE    := docker compose

.PHONY: help dev infra-up infra-down infra-reset migrate migrate-status seed run-api run-worker \
        test test-race test-db gen build lint evals webhook sentry-webhook runner-smoke tidy \
        web-install web web-live web-check

help:
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | sed 's/:.*## /\t/' | column -t -s $$'\t'

dev: infra-up migrate seed ## Start infra, migrate, seed (then run `make run-api` and `make run-worker`)
	@echo "Infra ready. Temporal UI: http://localhost:8233"
	@echo "Next: 'make run-api' and 'make run-worker' in two terminals, then 'make webhook'."

infra-up: ## Start Postgres, Valkey, Temporal (+ MinIO: --profile storage, LiteLLM: --profile llm)
	$(COMPOSE) up -d --wait postgres valkey temporal

infra-down: ## Stop the stack
	$(COMPOSE) down

infra-reset: ## Stop the stack and delete its volumes
	$(COMPOSE) down -v

migrate: ## Apply goose migrations (owner role)
	cd services/api && go run ./cmd/migrate up

migrate-status:
	cd services/api && go run ./cmd/migrate status

seed: ## Insert the dev org/user/installation/repo
	cd services/api && go run ./cmd/seed

run-api: ## Run the API + inbox processor on :8080
	cd services/api && go run ./cmd/api

run-worker: ## Run the Temporal worker (stub agents unless ALPINE_AGENT_MODE=llm)
	cd services/orchestrator && go run ./cmd/worker

webhook: ## Post a signed fake GitHub issues.opened webhook
	./scripts/send-test-webhook.sh $${N:-$$RANDOM}

sentry-webhook: ## Post a signed fake Sentry issue.created webhook (seeded installation/project)
	./scripts/send-test-sentry-webhook.sh $${N:-$$RANDOM}

web-install: ## pnpm install (web + TS client)
	pnpm install --frozen-lockfile

web: ## Web dev server on :5173 against MSW mocks (no backend needed)
	pnpm --filter @alpine/web dev

web-live: ## Web dev server on :5173 against the live API (Vite proxies /v1 to :8080)
	pnpm --filter @alpine/web dev:live

web-check: ## Web + TS client lint and typecheck
	pnpm -r lint
	pnpm -r typecheck

test: ## go vet + go test in every module (no network) + web lint/typecheck
	@set -e; for m in $(GO_MODULES); do echo "== $$m"; (cd $$m && go vet ./... && go test ./...); done
	$(MAKE) web-check

test-race: ## go test -race in every module
	@set -e; for m in $(GO_MODULES); do echo "== $$m"; (cd $$m && go test -race ./...); done

test-db: ## Postgres integration tests (fresh migrated DB per test: migrations, RLS, auth, repos)
	@set -e; export ALPINE_TEST_ADMIN_URL=$${ALPINE_TEST_ADMIN_URL:-postgres://alpine:alpine@localhost:$${POSTGRES_PORT:-5432}/postgres?sslmode=disable}; \
	  (cd packages/shared && go test -count=1 ./db/... ./repo/... ./auth/...); \
	  (cd services/api && go test -count=1 ./internal/authapi/...)

gen: ## Regenerate Go server (oapi-codegen) and TS client from api/openapi.yaml
	cd services/api && go generate ./internal/apigen
	pnpm --filter @alpine/api-client gen

build: ## Build all Go binaries into bin/
	@mkdir -p bin
	cd services/api && go build -o ../../bin/alpine-api ./cmd/api && go build -o ../../bin/alpine-migrate ./cmd/migrate && go build -o ../../bin/alpine-seed ./cmd/seed
	cd services/orchestrator && go build -o ../../bin/alpine-worker ./cmd/worker
	cd services/runner && go build -o ../../bin/runner-smoke ./cmd/runner-smoke
	@set -e; for m in $(GO_MODULES); do (cd $$m && go build ./...); done

lint:
	npx --yes @redocly/cli@latest lint api/openapi.yaml

evals: ## Offline agent evals (recorded responses, no network)
	cd packages/agents && go test ./evals/ -v

runner-smoke: ## Create a sandbox with the configured runner (Docker by default)
	cd services/runner && go run ./cmd/runner-smoke

tidy:
	@for m in $(GO_MODULES); do (cd $$m && go mod tidy); done
