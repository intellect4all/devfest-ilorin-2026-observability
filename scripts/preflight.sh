#!/usr/bin/env bash
# make preflight: the morning-of check. About 6 minutes. Ends with a GO/NO-GO
# summary and leaves the stack ready for Demo 1 (01-metrics, load on, bugs off).
#   - every branch image prebuilt and current
#   - stack up, seed present
#   - each stage switches cleanly, in talk order, under 15 s
#   - each bug toggles and shows up in the metrics
#   - ntfy reachable and a test alert delivered
. "$(dirname "$0")/lib.sh"
command -v jq >/dev/null || die "preflight needs jq"

results=()
record() { # record <GO|NO-GO> <what> <detail>
	results+=("$1|$2|$3")
	if [[ $1 == GO ]]; then ok "$2: $3"; else bad "$2: $3"; fi
}
prom() { curl -sf -G "$PROM/api/v1/query" --data-urlencode "query=$1" | jq -r '.data.result[0].value[1] // empty'; }
p99() { prom 'histogram_quantile(0.99, sum by (le) (rate(http_server_request_duration_seconds_bucket{job="transfers"}[30s])))'; }
ms() { awk -v v="${1:-0}" 'BEGIN { if (v == "" || v == "NaN") print "n/a"; else printf "%d\n", v * 1000 }'; }
start_branch=$(git rev-parse --abbrev-ref HEAD)
if ! git diff --quiet || ! git diff --cached --quiet; then die "commit or stash your changes first"; fi

info "1/5 images"
missing=""
for b in main 01-metrics 02-traces 03-logs 04-alerts hunt solution; do
	want="$b@$(git rev-parse --short "$b" 2>/dev/null)"
	have=$(docker image inspect "devfest26/app:$b" --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' 2>/dev/null)
	[[ $have == "$want" ]] || missing+="$b "
done
if [[ -z $missing ]]; then record GO images "all 7 branches prebuilt and current"; else record NO-GO images "rebuild with make prebuild: $missing"; fi

info "2/5 stack and seed"
make -s stage STAGE=01-metrics >/dev/null 2>&1 || make -s up >/dev/null 2>&1
rows=$(psql_q "SELECT count(*) FROM entries" 2>/dev/null)
if ((${rows:-0} > 1000000)); then record GO seed "$rows ledger entries"; else record NO-GO seed "only ${rows:-0} entries: make seed"; fi
make -s bugs-off >/dev/null
make -s load >/dev/null

info "3/5 stage switches in talk order"
for b in 01-metrics 02-traces 03-logs hunt 04-alerts 01-metrics; do
	f=$(mktemp)
	if STAGE_TIME_FILE=$f make -s stage STAGE=$b >/dev/null 2>&1 && scripts/check.sh >/dev/null 2>&1; then
		secs=$(cat "$f")
		if awk -v s="$secs" 'BEGIN { exit !(s < 15) }'; then record GO "stage $b" "${secs}s, make check all ticks"; else record NO-GO "stage $b" "${secs}s (over 15 s)"; fi
	else
		record NO-GO "stage $b" "switch or make check failed (make stage STAGE=$b to see why)"
	fi
	rm -f "$f"
done

info "4/5 each bug shows up (on 01-metrics, under load)"
sleep 30
base=$(ms "$(p99)")
for bug in c b a; do
	make -s "bug-$bug-on" >/dev/null
	surge_pid=""
	if [[ $bug == a ]]; then
		make -s surge SURGE_SECONDS=50 >/dev/null 2>&1 &
		surge_pid=$!
	fi
	sleep 40
	now_p99=$(ms "$(p99)")
	plan=""
	[[ $bug == b ]] && plan=" ($(make -s explain 2>/dev/null | grep -oE 'Seq Scan|Index Only Scan|Index Scan' | head -1))"
	make -s bugs-off >/dev/null
	[[ -n $surge_pid ]] && wait "$surge_pid" 2>/dev/null
	if [[ $now_p99 != n/a ]] && ((now_p99 > 300)); then
		record GO "bug $bug" "p99 ${base} ms → ${now_p99} ms$plan"
	else
		record NO-GO "bug $bug" "p99 ${base} ms → ${now_p99} ms$plan: too mild, see RUNBOOK calibration"
	fi
	docker compose restart transfers >/dev/null 2>&1
	wait_apps 60 >/dev/null
	sleep 25
done

info "5/5 ntfy"
make -s stage STAGE=04-alerts >/dev/null 2>&1
if make -s alert-test >/dev/null 2>&1; then
	record GO ntfy "test alert sent to ${NTFY_URL%/}/$NTFY_TOPIC: check your phone buzzed"
else
	record NO-GO ntfy "make alert-test failed: check NTFY_URL/NTFY_TOPIC in .env, or use Plan B (make ntfy-local)"
fi

# Ready for Demo 1.
make -s stage STAGE=01-metrics >/dev/null 2>&1
make -s bugs-off >/dev/null

echo
echo "=================== PREFLIGHT $(date '+%H:%M') ==================="
nogo=0
for r in "${results[@]}"; do
	IFS='|' read -r s what detail <<<"$r"
	printf '  %-7s %-18s %s\n' "$s" "$what" "$detail"
	[[ $s == GO ]] || nogo=$((nogo + 1))
done
echo "=========================================================="
if ((nogo == 0)); then
	ok "GO. Stack is on 01-metrics with load running and every bug off. Ready for Demo 1."
else
	bad "NO-GO: $nogo item(s). See docs/RUNBOOK.md, Recovery playbook."
	exit 1
fi
[[ $start_branch == 01-metrics ]] || info "(you started on $start_branch; you are now on 01-metrics)"
