#!/usr/bin/env bash
# Maintainer tool: regenerate grafana/dashboards/payments.json on every
# branch from tools/dashboard.py, committing where it changed.
set -euo pipefail
cd "$(dirname "$0")/.."
back=$(git rev-parse --abbrev-ref HEAD)
for spec in "01-metrics:" "02-traces:--traces" "03-logs:--traces" "04-alerts:--traces --alerts" "hunt:--traces --alerts" "solution:--traces --alerts"; do
	b=${spec%%:*} flags=${spec#*:}
	git rev-parse --verify -q "refs/heads/$b" >/dev/null || continue
	git checkout -q "$b"
	# shellcheck disable=SC2086
	python3 tools/dashboard.py $flags >grafana/dashboards/payments.json
	if ! git diff --quiet; then
		git commit -qam "$b: regenerate the dashboard

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
		echo "$b: dashboard regenerated"
	fi
done
git checkout -q "$back"
