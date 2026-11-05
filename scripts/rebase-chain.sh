#!/usr/bin/env bash
# Maintainer tool: after committing a change on a branch, replay every later
# branch on top of it so the chain stays cumulative:
#   main -> 01-metrics -> 02-traces -> 03-logs -> 04-alerts -> {hunt, solution}
#
# Usage: scripts/rebase-chain.sh <changed-branch> <its-old-tip>
#   e.g. git rev-parse main >/tmp/old; (commit on main); scripts/rebase-chain.sh main $(cat /tmp/old)
# Each child is rebased with --onto, so only its own commits are replayed.
set -euo pipefail
cd "$(dirname "$0")/.."
changed=${1:?changed branch}
old_tip=${2:?old tip of the changed branch}
back=$(git rev-parse --abbrev-ref HEAD)

parent_of() {
	case $1 in
	01-metrics) echo main ;; 02-traces) echo 01-metrics ;; 03-logs) echo 02-traces ;;
	04-alerts) echo 03-logs ;; hunt | solution) echo 04-alerts ;; esac
}
order=(01-metrics 02-traces 03-logs 04-alerts hunt solution)

# regen_dashboard <branch>: rebuild payments.json from tools/dashboard.py
# with the branch's flags and fold any change into the branch's last commit.
regen_dashboard() {
	local flags
	case $1 in
	01-metrics) flags="" ;; 02-traces | 03-logs) flags="--traces" ;; *) flags="--traces --alerts" ;;
	esac
	[[ -f grafana/dashboards/payments.json ]] || return 0
	# shellcheck disable=SC2086
	python3 tools/dashboard.py $flags >grafana/dashboards/payments.json
	git diff --quiet || git commit -q -a --amend --no-edit
}

declare_old() { eval "OLD_${1//-/_}=$2"; }
get_old() { eval "echo \${OLD_${1//-/_}:-}"; }
for b in main "${order[@]}"; do
	git rev-parse --verify -q "refs/heads/$b" >/dev/null && declare_old "$b" "$(git rev-parse "$b")"
done
declare_old "$changed" "$old_tip"

started=false
[[ $changed == main ]] && started=true
for b in "${order[@]}"; do
	git rev-parse --verify -q "refs/heads/$b" >/dev/null || continue
	p=$(parent_of "$b")
	if ! $started; then
		[[ $b == "$changed" ]] && started=true
		continue
	fi
	# -X theirs: payments.json is generated, so a conflict there is settled
	# by regenerating it right after; elsewhere the branch's own change wins.
	git rebase -q -X theirs --onto "$p" "$(get_old "$p")" "$b"
	regen_dashboard "$b"
	# -X theirs can merge cleanly into code that does not compile: check.
	go build ./... && go vet ./... || { echo "$b does not build after the rebase: fix it, then rerun from $b"; exit 1; }
	echo "rebased $b onto $p"
done
git checkout -q "$back"
