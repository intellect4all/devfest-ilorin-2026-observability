# DevFest Ilorin 2026: Observability for Distributed Systems.
# `make help` lists everything. Settings come from .env (cp .env.example .env).
SHELL := /bin/bash
.DEFAULT_GOAL := help

-include .env
export

# The image tag is the git branch, so each branch runs its own prebuilt image.
STAGE ?= $(shell git rev-parse --abbrev-ref HEAD 2>/dev/null || echo main)
export STAGE
export COMPOSE_PROJECT_NAME := devfest26

ROWS ?= 7000000
HISTORY ?= 80000
LOAD_RATE ?= 20
LOAD_HOT ?= 0.9
SURGE_RATE ?= 80
SURGE_SECONDS ?= 90
TRANSFERS_PORT ?= 8080
KEY ?= $(shell uuidgen 2>/dev/null || cat /proc/sys/kernel/random/uuid)
FROM ?= acct-00002
TO ?= acct-00003
AMOUNT ?= 2500

DC := docker compose
T_ADMIN := http://127.0.0.1:7080/admin/bugs
L_ADMIN := http://127.0.0.1:7081/admin/bugs
P_ADMIN := http://127.0.0.1:7082/admin/config

.PHONY: help up down reset seed check logs load load-stop load-logs surge tx \
	bug-a-on bug-a-off bug-b-on bug-b-off bug-c-on bug-c-off bugs-off bugs explain \
	provider-flaky provider-normal alert-test subscribe ntfy-local stage prebuild preflight \
	verify offline-kit calibrate test

help: ## List targets
	@awk 'BEGIN {FS = ":.*## "} /^##@/ {printf "\n%s\n", substr($$0, 5)} /^[a-z0-9-]+:.*## / {printf "  %-16s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

##@ Setup
up: ## Start the stack for this branch
	@scripts/up.sh

down: ## Stop the stack (keeps data)
	@$(DC) --profile load --profile offline down

reset: ## Stop the stack and delete its data (this project's volumes only)
	@$(DC) --profile load --profile offline down -v --remove-orphans

seed: ## Load ROWS ledger entries and HISTORY recent transfers (defaults 7,000,000 / 80,000)
	@$(DC) exec -T postgres psql -q -U devfest -d devfest -v rows=$(ROWS) -v history=$(HISTORY) -f /scripts/seed.sql
	@$(DC) restart transfers ledger >/dev/null && source scripts/lib.sh && wait_apps 120 && ok "seeded; services restarted"

check: ## One tick or cross per component
	@scripts/check.sh

logs: ## Follow the services' logs
	@$(DC) logs -f --tail=20

test: ## go build, vet and test inside the pinned Go image
	@scripts/gotest.sh

##@ Traffic
load: ## Start steady traffic in the background (LOAD_RATE req/s, LOAD_HOT share from acct-00001)
	@$(DC) --profile load up -d --no-build loadgen && echo "loadgen: $(LOAD_RATE) req/s. make load-logs to watch, make load-stop to stop"

load-stop: ## Stop steady traffic
	@$(DC) --profile load stop loadgen >/dev/null && $(DC) --profile load rm -f loadgen >/dev/null; echo "loadgen stopped"

load-logs: ## Watch the loadgen's client-side numbers
	@$(DC) --profile load logs -f --tail=5 loadgen

surge: ## Add SURGE_RATE req/s on top for SURGE_SECONDS, then stop
	@$(DC) --profile load run --rm --no-deps loadgen /app/loadgen -url=http://transfers:8080/transfers \
		-rate=$(SURGE_RATE) -duration=$(SURGE_SECONDS)s -hot=0 -label=surge

tx: ## Send one transfer (KEY=... to replay the same Idempotency-Key)
	@echo "Idempotency-Key: $(KEY)"
	@curl -s -w '  HTTP %{http_code} in %{time_total}s\n' -X POST http://127.0.0.1:$(TRANSFERS_PORT)/transfers \
		-H 'Content-Type: application/json' -H 'Idempotency-Key: $(KEY)' \
		-d '{"from":"$(FROM)","to":"$(TO)","amount":$(AMOUNT)}'

##@ Faults (presenter only; never visible in telemetry)
bug-a-on: ## Bug A: O(n) duplicate check
	@curl -sf -X POST '$(T_ADMIN)?a=on'
bug-a-off:
	@curl -sf -X POST '$(T_ADMIN)?a=off'
bug-b-on: ## Bug B: drop the balance-query index
	@curl -sf -X POST '$(L_ADMIN)?b=on'
bug-b-off:
	@curl -sf -X POST '$(L_ADMIN)?b=off'
bug-c-on: ## Bug C: hold the account lock across the payout call
	@curl -sf -X POST '$(T_ADMIN)?c=on'
bug-c-off:
	@curl -sf -X POST '$(T_ADMIN)?c=off'
bugs-off: ## Turn every bug off and make the provider healthy again
	@curl -sf -X POST '$(T_ADMIN)?a=off&c=off' >/dev/null && curl -sf -X POST '$(L_ADMIN)?b=off' >/dev/null && \
		curl -sf -X POST '$(P_ADMIN)?error_rate=0&timeout_rate=0' >/dev/null && echo "all bugs off, provider normal"
bugs: ## Show which bugs are on
	@echo "transfers: $$(curl -sf $(T_ADMIN))  ledger: $$(curl -sf $(L_ADMIN))  provider: $$(curl -sf $(P_ADMIN))"

explain: ## Show the balance query's plan (Index Scan = healthy, Seq Scan = bug B)
	@$(DC) exec -T postgres psql -U devfest -d devfest -c \
		"EXPLAIN (ANALYZE, BUFFERS, COSTS OFF) SELECT COALESCE(SUM(amount), 0)::bigint FROM entries WHERE account_id = 'acct-00042';"

provider-flaky: ## Provider rejects 20% of payouts (5xx alert demo)
	@curl -sf -X POST '$(P_ADMIN)?error_rate=0.2'
provider-normal: ## Provider healthy again
	@curl -sf -X POST '$(P_ADMIN)?error_rate=0&timeout_rate=0'

##@ Alerts
alert-test: ## Send a sample alert through the relay to ntfy
	@scripts/alert-test.sh

subscribe: ## Show the ntfy server and topic big, for the audience to subscribe
	@printf '\n\n   Open the ntfy app  →  +  →  subscribe to:\n\n'
	@printf '   server   %s\n\n   topic    %s\n\n\n' "$${NTFY_URL:-https://ntfy.sh}" "$${NTFY_TOPIC:?set NTFY_TOPIC in .env}" | sed 's/./& /g'

ntfy-local: ## Offline Plan B: start a local ntfy server phones can reach on the hotspot
	@$(DC) --profile offline up -d ntfy && echo "ntfy on http://$$(ipconfig getifaddr en0 2>/dev/null || hostname -I | cut -d' ' -f1):$${NTFY_LOCAL_PORT:-2586} - set NTFY_URL=http://ntfy in .env and make up"

##@ Stage
stage: ## Switch to a prebuilt branch without compiling: make stage STAGE=02-traces
	@scripts/stage.sh "$(STAGE)"

prebuild: ## Build devfest26/app:<branch> for every branch
	@scripts/prebuild.sh

preflight: ## Morning-of go/no-go check
	@scripts/preflight.sh

calibrate: ## Measure bug impact on this machine and suggest ROWS/HISTORY
	@scripts/calibrate.sh

##@ Proof
verify: ## Re-run every check end to end and write a new evidence/ folder
	@scripts/verify.sh

##@ Offline
offline-kit: ## Vendor modules and save every image to offline-kit/ for a USB stick
	@scripts/offline-kit.sh
