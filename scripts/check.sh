#!/usr/bin/env bash
# make check: one tick or cross per component. Exits 1 if anything is down.
. "$(dirname "$0")/lib.sh"

fails=0
check() { # check <label> <command...>
	local label=$1
	shift
	if "$@" >/dev/null 2>&1; then ok "$label"; else bad "$label"; fails=$((fails + 1)); fi
}

echo "devfest26 on branch $STAGE"
check "docker engine" docker info
check "postgres" dc exec -T postgres pg_isready -q -U devfest -d devfest
seeded() { [[ $(psql_q "SELECT count(*) > 1000 FROM entries") == t ]]; }
check "seed data (make seed)" seeded
check "ledger      :$LEDGER_PORT" http_ok "http://127.0.0.1:$LEDGER_PORT/healthz"
check "provider    :$PROVIDER_PORT" http_ok "http://127.0.0.1:$PROVIDER_PORT/healthz"
check "transfers   :$TRANSFERS_PORT" http_ok "http://127.0.0.1:$TRANSFERS_PORT/healthz"
check "bug toggles (admin, localhost only)" http_ok "http://127.0.0.1:7080/admin/bugs"
check "grafana     $GRAFANA" http_ok "$GRAFANA/api/health"
check "prometheus  :$PROM_PORT" http_ok "$PROM/-/ready"
check "tempo       :$TEMPO_PORT" http_ok "http://127.0.0.1:$TEMPO_PORT/ready"
check "loki        :$LOKI_PORT" http_ok "http://127.0.0.1:$LOKI_PORT/ready"
check "otel collector" dc exec -T lgtm curl -sf -o /dev/null http://127.0.0.1:13133/ready
if [[ " $APP_SERVICES " == *" alertrelay "* ]]; then
	check "alertrelay  :$RELAY_PORT" http_ok "http://127.0.0.1:$RELAY_PORT/healthz"
fi
if [[ -n "${NTFY_URL:-}" ]]; then
	ntfy_health="${NTFY_URL%/}/v1/health"
	[[ $NTFY_URL == http://ntfy ]] && ntfy_health="http://127.0.0.1:$NTFY_LOCAL_PORT/v1/health"
	check "ntfy        ${NTFY_URL%/}/${NTFY_TOPIC:-?}" http_ok "$ntfy_health"
else
	bad "ntfy        (NTFY_URL not set: cp .env.example .env)"
	fails=$((fails + 1))
fi

if ((fails)); then
	bad "$fails check(s) failed"
	exit 1
fi
ok "all good"
