#!/usr/bin/env bash
# make alert-test: send a sample alert through the relay to ntfy, the same
# path a real alert takes after Grafana.
. "$(dirname "$0")/lib.sh"
[[ " $APP_SERVICES " == *" alertrelay "* ]] || die "alerting arrives on the 04-alerts branch (make stage STAGE=04-alerts)"
[[ -n ${NTFY_TOPIC:-} ]] || die "NTFY_TOPIC is not set: cp .env.example .env and pick a topic"
out=$(curl -sf -X POST "http://127.0.0.1:$RELAY_PORT/test") || die "relay did not accept the test alert (docker compose logs alertrelay)"
ok "sent: $out"
echo "Phones subscribed to ${NTFY_URL%/}/$NTFY_TOPIC should buzz now."
