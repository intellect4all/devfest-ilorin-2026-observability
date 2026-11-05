# Shared helpers for the workshop scripts. Source it: . scripts/lib.sh
# shellcheck shell=bash

ROOT="${ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT" || exit 1

# .env holds the ntfy topic and any port overrides; it is never committed.
if [[ -f .env ]]; then
	set -a
	# shellcheck disable=SC1091
	. ./.env
	set +a
fi

export STAGE="${STAGE:-$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo main)}"
export COMPOSE_PROJECT_NAME=devfest26
GRAFANA_PORT="${GRAFANA_PORT:-3000}"
PROM_PORT="${PROM_PORT:-9090}"
TEMPO_PORT="${TEMPO_PORT:-3200}"
LOKI_PORT="${LOKI_PORT:-3100}"
TRANSFERS_PORT="${TRANSFERS_PORT:-8080}"
LEDGER_PORT="${LEDGER_PORT:-8081}"
PROVIDER_PORT="${PROVIDER_PORT:-8082}"
RELAY_PORT="${RELAY_PORT:-8090}"
NTFY_LOCAL_PORT="${NTFY_LOCAL_PORT:-2586}"
GRAFANA="http://127.0.0.1:${GRAFANA_PORT}"
PROM="http://127.0.0.1:${PROM_PORT}"

# app_services: the services built from this repo, read from this branch's
# compose file (04-alerts adds alertrelay).
app_services() {
	docker compose config --services 2>/dev/null | grep -vxE 'postgres|lgtm' | tr '\n' ' '
}
APP_SERVICES="$(app_services)"

if [[ -t 1 ]]; then GREEN=$'\e[32m' RED=$'\e[31m' DIM=$'\e[2m' OFF=$'\e[0m'; else GREEN='' RED='' DIM='' OFF=''; fi
ok() { printf '%s✓%s %s\n' "$GREEN" "$OFF" "$*"; }
bad() { printf '%s✗%s %s\n' "$RED" "$OFF" "$*"; }
info() { printf '%s%s%s\n' "$DIM" "$*" "$OFF"; }
die() { bad "$*"; exit 1; }

dc() { docker compose "$@"; }

# wait_for <seconds> <description> <command...>: retry until the command succeeds.
wait_for() {
	local timeout=$1 what=$2
	shift 2
	local deadline=$((SECONDS + timeout))
	until "$@" >/dev/null 2>&1; do
		((SECONDS < deadline)) || { bad "timed out after ${timeout}s waiting for $what"; return 1; }
		sleep 0.5
	done
}

http_ok() { curl -sf -o /dev/null --max-time 3 "$1"; }

# now: seconds since the epoch with milliseconds (macOS date has no %N).
now() { perl -MTime::HiRes=time -e 'printf "%.3f\n", time'; }

# wait_apps: every app service answers /healthz.
wait_apps() {
	local s port
	for s in $APP_SERVICES; do
		case $s in
		transfers) port=$TRANSFERS_PORT ;;
		ledger) port=$LEDGER_PORT ;;
		provider) port=$PROVIDER_PORT ;;
		alertrelay) port=$RELAY_PORT ;;
		*) continue ;;
		esac
		wait_for "${1:-60}" "$s" http_ok "http://127.0.0.1:${port}/healthz" || return 1
	done
}

# grafana_reload: re-read provisioned dashboards, datasources and alert rules.
grafana_reload() {
	local k
	for k in datasources dashboards alerting; do
		curl -sf -u admin:admin -X POST "$GRAFANA/api/admin/provisioning/$k/reload" >/dev/null ||
			{ bad "grafana $k reload failed"; return 1; }
	done
}

# psql_q <sql>: run a query in the stack's Postgres and print the bare result.
psql_q() { dc exec -T postgres psql -qtAX -U devfest -d devfest -c "$1"; }
