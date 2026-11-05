#!/usr/bin/env bash
# Maintainer tool: after committing a change on a branch, replay every later
# branch on top of it so the chain stays cumulative:
#   main -> 01-metrics -> 02-traces -> 03-logs -> 04-alerts -> {hunt, solution}
# Usage: scripts/rebase-chain.sh [first-branch-to-rebase]
set -euo pipefail
cd "$(dirname "$0")/.."
chain=(main 01-metrics 02-traces 03-logs 04-alerts)
from=${1:-01-metrics}
started=false
back=$(git rev-parse --abbrev-ref HEAD)
for ((i = 1; i < ${#chain[@]}; i++)); do
	[[ ${chain[i]} == "$from" ]] && started=true
	$started || continue
	git rev-parse --verify -q "refs/heads/${chain[i]}" >/dev/null || continue
	git rebase -q "${chain[i - 1]}" "${chain[i]}"
	echo "rebased ${chain[i]} onto ${chain[i - 1]}"
done
for leaf in hunt solution; do
	git rev-parse --verify -q "refs/heads/$leaf" >/dev/null || continue
	git rebase -q 04-alerts "$leaf"
	echo "rebased $leaf onto 04-alerts"
done
git checkout -q "$back"
