#!/usr/bin/env bash
# make up: start Postgres, the LGTM stack and this branch's services.
# Builds devfest26/app:<branch> only if it does not exist yet (make prebuild
# builds every branch ahead of time).
. "$(dirname "$0")/lib.sh"

start=$SECONDS
if ! docker image inspect "devfest26/app:$STAGE" >/dev/null 2>&1; then
	info "image devfest26/app:$STAGE not found, building it (make prebuild avoids this)"
	dc build transfers || die "build failed"
fi
dc up -d --wait postgres lgtm || die "postgres or lgtm did not become healthy"
# shellcheck disable=SC2086
dc up -d --no-build $APP_SERVICES || die "starting $APP_SERVICES failed"
wait_apps 90 || die "services did not come up (docker compose logs)"
ok "stack up on $STAGE in $((SECONDS - start))s: Grafana $GRAFANA"
