SHELL := /bin/bash
ROWS ?= 3000000
ID ?=

# The transfers service runs on your machine (make dev) so you can edit it live.
DEV_ENV = PORT=8080 \
	DATABASE_URL=postgres://devfest:devfest@localhost:5432/devfest \
	LEDGER_URL=http://localhost:8081 \
	PROVIDER_URL=http://localhost:8082 \
	OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318

IMAGES = postgres:16-alpine jaegertracing/all-in-one:1.62.0 prom/prometheus:v2.54.1 \
	grafana/grafana:11.2.0 golang:1.23-alpine gcr.io/distroless/static-debian12

.PHONY: help tidy pull up down reset dev check seed load tx bug-on bug-off explain logs \
	provider-flaky provider-normal branches vendor offline

help: ## List targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-16s %s\n", $$1, $$2}'

tidy: ## Resolve Go modules (run once after cloning)
	@go mod tidy

pull: tidy ## Pull and build every image (do this on good internet)
	@docker compose pull --ignore-buildable && docker compose build

up: ## Start Postgres, ledger, mockprovider, Jaeger, Prometheus, Grafana
	@docker compose up -d --build

down: ## Stop the stack (keeps data)
	@docker compose down

reset: ## Stop the stack and delete all data
	@docker compose down -v; rm -f .transfers.log

dev: ## Run the transfers service locally (the one you edit)
	@$(DEV_ENV) go run ./transfers 2>&1 | tee -a .transfers.log

check: ## Verify your setup
	@go version >/dev/null 2>&1 && echo "✓ go toolchain" || echo "✗ go toolchain (install Go 1.23+)"
	@go build -o /dev/null ./transfers 2>/dev/null && echo "✓ transfers compiles" || echo "✗ transfers does not compile (run make tidy)"
	@docker compose exec -T postgres pg_isready -q -U devfest 2>/dev/null && echo "✓ postgres" || echo "✗ postgres (run make up)"
	@curl -sf localhost:8081/healthz >/dev/null && echo "✓ ledger" || echo "✗ ledger"
	@curl -sf localhost:8082/healthz >/dev/null && echo "✓ mockprovider" || echo "✗ mockprovider"
	@curl -sf localhost:16686 >/dev/null && echo "✓ jaeger      http://localhost:16686" || echo "✗ jaeger"
	@curl -sf localhost:9090/-/ready >/dev/null && echo "✓ prometheus  http://localhost:9090" || echo "✗ prometheus"
	@curl -sf localhost:3000/api/health >/dev/null && echo "✓ grafana     http://localhost:3000" || echo "✗ grafana"

seed: ## Load ROWS ledger entries (default 3,000,000; takes about a minute)
	@docker compose exec -T postgres psql -q -U devfest -d devfest -v rows=$(ROWS) -f /scripts/seed.sql

load: ## Steady traffic against transfers until Ctrl-C
	@go run ./loadgen

tx: ## Send one transfer
	@curl -s -X POST localhost:8080/transfers -H 'Content-Type: application/json' \
		-H "Idempotency-Key: tx-$$RANDOM$$RANDOM" -d '{"from":"acct-00001","to":"acct-00002","amount":2500}'; echo

bug-on: ## (presenter) break production
	@curl -sf -X POST 'localhost:8081/admin/fault?slow_balance=on' >/dev/null && echo "fault on"

bug-off: ## (presenter) fix production
	@curl -sf -X POST 'localhost:8081/admin/fault?slow_balance=off' >/dev/null && echo "fault off"

explain: ## Show the query plan for the balance lookup
	@docker compose exec -T postgres psql -U devfest -d devfest -c \
		"EXPLAIN ANALYZE SELECT COALESCE(SUM(amount), 0)::bigint FROM entries WHERE account_id = 'acct-00042';"

logs: ## Every log line for one trace: make logs ID=<trace_id>
	@test -n "$(ID)" || { echo "usage: make logs ID=<trace_id>"; exit 1; }
	@{ docker compose logs --no-log-prefix ledger mockprovider; cat .transfers.log 2>/dev/null; } | grep "$(ID)"

provider-flaky: ## Take-home: 30% of payouts hang past the client timeout
	@curl -sf -X POST 'localhost:8082/admin/config?timeout_rate=0.3'; echo

provider-normal: ## Take-home: provider behaves again
	@curl -sf -X POST 'localhost:8082/admin/config?timeout_rate=0&error_rate=0'; echo

branches: ## (presenter) build main / checkpoint-1 / solution from this tree
	@bash scripts/make-branches.sh

vendor: ## Vendor Go modules for the offline USB kit
	@go mod vendor

offline: vendor ## Save every image to devfest-images.tar for the USB kit
	@docker save -o devfest-images.tar $(IMAGES) devfest-observability-ledger devfest-observability-mockprovider
	@echo "Copy the repo folder and devfest-images.tar to USB. Attendees: docker load -i devfest-images.tar"
