#!/usr/bin/env bash
# The audience hunt (hunt branch).
#   scripts/hunt.sh mystery [a|b|c]   one bug on, which one stays secret
#   scripts/hunt.sh hint              the next hint from the ladder
#   scripts/hunt.sh reveal            say which bug it was, turn it off
# The answer is kept in .hunt/answer (git-ignored), never in any signal.
. "$(dirname "$0")/lib.sh"
mkdir -p .hunt
T_ADMIN=http://127.0.0.1:7080/admin/bugs
L_ADMIN=http://127.0.0.1:7081/admin/bugs
SURGE_NAME=devfest26-hunt-surge

stop_surge() { docker rm -f "$SURGE_NAME" >/dev/null 2>&1 || true; }

case ${1:-} in
mystery)
	stop_surge
	make -s bugs-off >/dev/null
	make -s load >/dev/null
	bug=${2:-}
	[[ -z $bug ]] && bug=$(printf 'a\nb\nc\n' | sed -n "$((RANDOM % 3 + 1))p")
	case $bug in
	a)
		curl -sf -X POST "$T_ADMIN?a=on" >/dev/null
		# This bug only shows under traffic, so the hunt comes with a rush.
		dc --profile load run -d --rm --no-deps --name "$SURGE_NAME" loadgen /app/loadgen \
			-url=http://transfers:8080/transfers -rate="${SURGE_RATE:-80}" -duration=15m -hot=0 -label=surge >/dev/null
		;;
	b) curl -sf -X POST "$L_ADMIN?b=on" >/dev/null ;;
	c) curl -sf -X POST "$T_ADMIN?c=on" >/dev/null ;;
	*) die "usage: make mystery [BUG=a|b|c]" ;;
	esac
	echo "$bug $(date +%s)" >.hunt/answer
	rm -f .hunt/hints
	echo "A bug is live in production. Something is wrong, somewhere, for some reason."
	echo "Find all three with the signals only. Rules and clues: HUNT.md"
	echo "Grafana: $GRAFANA/d/payments"
	;;
hint)
	[[ -f .hunt/answer ]] || die "no hunt running: make mystery"
	n=$(($(cat .hunt/hints 2>/dev/null || echo 0) + 1))
	echo "$n" >.hunt/hints
	case $n in
	1) echo "Hint 1 (what): which RED tile went red? Which USE panel moved with it, and which did not?" ;;
	2) echo "Hint 2 (where): click a diamond (exemplar) on the p99 line, or a trace ID in 'Slowest transfers'." ;;
	3) echo "Hint 3 (where): in the trace, find the span with the most time of its own: wide, with nothing under it." ;;
	4) echo "Hint 4 (why): on that span, open 'Logs for this span'. Read the WARN line; every number in it matters." ;;
	*) echo "No more hints. make reveal when you are ready." ;;
	esac
	;;
reveal)
	[[ -f .hunt/answer ]] || die "no hunt running: make mystery"
	read -r bug started <.hunt/answer
	echo "Hunted for $((($(date +%s) - started) / 60)) min."
	case $bug in
	a) echo "Bug A: the duplicate check walks every transfer of the last 24 h, lower-casing each key, under one lock." ;;
	b) echo "Bug B: the index on entries(account_id) is gone; every balance check is a sequential scan (make explain)." ;;
	c) echo "Bug C: the account lock is held across the provider call; every payout from the wallet queues behind the last." ;;
	esac
	stop_surge
	make -s bugs-off
	rm -f .hunt/answer .hunt/hints
	;;
*) die "usage: scripts/hunt.sh mystery|hint|reveal" ;;
esac
