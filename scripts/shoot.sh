#!/usr/bin/env bash
# scripts/shoot.sh <shoot.mjs args>: take an evidence screenshot with
# Playwright inside the pinned Playwright image (repo-local node_modules,
# host networking so it can reach Grafana on 127.0.0.1). Output paths must be
# under the repo or under $SHOOT_OUT_DIR.
. "$(dirname "$0")/lib.sh"
PW_IMAGE=mcr.microsoft.com/playwright:v1.63.0-noble
out_dir=${SHOOT_OUT_DIR:-$ROOT}
if [[ ! -d tools/evidence/node_modules/playwright ]]; then
	docker run --rm --user "$(id -u):$(id -g)" -e HOME=/tmp -v "$ROOT/tools/evidence":/w -w /w "$PW_IMAGE" npm install --no-audit --no-fund -s >/dev/null ||
		die "npm install in tools/evidence failed"
fi
docker run --rm --network host --user "$(id -u):$(id -g)" -e HOME=/tmp \
	-e GRAFANA_URL="$GRAFANA" -v "$ROOT/tools/evidence":/w:ro -v "$out_dir":"$out_dir" -w "$out_dir" \
	"$PW_IMAGE" node /w/shoot.mjs "$@"
