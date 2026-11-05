#!/usr/bin/env bash
# make stage STAGE=<branch>: switch the running demo to another branch
# without compiling anything.
#   1. git checkout <branch>
#   2. swap the app containers to the prebuilt devfest26/app:<branch> image
#      (Postgres and the LGTM container keep running, with all their data)
#   3. ask Grafana to re-read dashboards, datasources and alert rules
. "$(dirname "$0")/lib.sh"

target=${1:-}
[[ -n $target ]] || die "usage: make stage STAGE=02-traces"
t0=$(now)

git rev-parse --verify -q "refs/heads/$target" >/dev/null || die "no branch called $target"
docker image inspect "devfest26/app:$target" >/dev/null 2>&1 || die "devfest26/app:$target is not built: run make prebuild"
if ! git diff --quiet || ! git diff --cached --quiet; then
	die "uncommitted changes would be carried across: git stash first"
fi

git checkout -q "$target" || die "git checkout $target failed"
export STAGE=$target
APP_SERVICES="$(app_services)"

# Only containers whose image or settings changed are recreated.
# shellcheck disable=SC2086
dc up -d --no-build --remove-orphans postgres lgtm $APP_SERVICES 2>&1 | grep -vE 'Running|Waiting|Healthy' || true
wait_apps 30 || die "services did not come back (docker compose logs)"
grafana_reload || die "Grafana did not reload its provisioning"

t1=$(now)
secs=$(awk -v a="$t0" -v b="$t1" 'BEGIN { printf "%.1f", b - a }')
ok "on $target in ${secs}s  (bugs A and C reset to off; bug B: $(curl -sf http://127.0.0.1:7081/admin/bugs))"
echo "$secs" >"${STAGE_TIME_FILE:-/dev/null}"
