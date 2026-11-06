#!/usr/bin/env bash
# make verify: rerun every check end to end and write a new evidence folder.
#
#   ONLY="main 01-metrics" make verify    just some sections
#   EVIDENCE_ROOT=/path make verify       write evidence elsewhere
#
# Works in a scratch clone so branch switches never touch your working tree,
# and runs from a copy of the scripts so checkouts cannot change the script
# while bash is reading it. Takes about 90 minutes for everything.
set -uo pipefail

if [[ -z ${VERIFY_STAGE2:-} ]]; then
	PRIMARY="$(cd "$(dirname "$0")/.." && pwd)"
	scratch=$(mktemp -d "${TMPDIR:-/tmp}/devfest26-verify.XXXXXX")
	git clone -q "$PRIMARY" "$scratch/repo" || exit 1
	for b in main 01-metrics 02-traces 03-logs 04-alerts hunt solution; do
		git -C "$PRIMARY" rev-parse --verify -q "refs/heads/$b" >/dev/null &&
			git -C "$scratch/repo" branch -q -f "$b" "$(git -C "$PRIMARY" rev-parse "$b")" 2>/dev/null
	done
	git -C "$scratch/repo" checkout -q main
	[[ -f $PRIMARY/.env ]] && cp "$PRIMARY/.env" "$scratch/repo/.env"
	[[ -d $PRIMARY/tools/evidence/node_modules ]] && cp -R "$PRIMARY/tools/evidence/node_modules" "$scratch/repo/tools/evidence/"
	cp -R "$PRIMARY/scripts" "$scratch/bin"
	export VERIFY_STAGE2=1 PRIMARY ROOT="$scratch/repo" SCRATCH="$scratch"
	exec bash "$scratch/bin/verify.sh" "$@"
fi

BIN="$(cd "$(dirname "$0")" && pwd)"
. "$BIN/lib.sh"
. "$BIN/evidence.sh"
. "$BIN/verify-checks.sh"
command -v jq >/dev/null || die "verify needs jq"

ONLY=${ONLY:-"main 01-metrics 02-traces 03-logs 04-alerts hunt solution stage offline"}
want() { [[ " $ONLY " == *" $1 "* ]]; }
ROWS=${ROWS:-7000000}
HISTORY=${HISTORY:-80000}

# goto <branch>: check out a branch in the scratch clone and refresh settings.
goto() {
	git checkout -q "$1" || die "checkout $1"
	export STAGE=$1
	APP_SERVICES="$(app_services)"
}

# settle <seconds> <why>: wait, saying why.
settle() { info "   waiting ${1}s: $2"; sleep "$1"; }

# --- checks every branch gets ------------------------------------------------

common_checks() {
	local b=$1
	EV_CMD="make test"
	EV_EXPECT="go build, go vet and go test pass in golang:1.26.8-alpine"
	ev_run make-test make test
	local st=PASS
	grep -qE '^ok[[:space:]]+devfest/transfers' "$EV_OUT" || st=FAIL
	grep -qE '^(FAIL|--- FAIL)|^# exit [1-9]' "$EV_OUT" && st=FAIL
	EV_ACTUAL="$(grep -cE '^ok[[:space:]]' "$EV_OUT") packages ok, $(grep -cE '^(FAIL|--- FAIL)' "$EV_OUT") failures"
	EV_FILES="$EV_OUT"
	ev_check "$st" "go build / vet / test"

	local t0 up_s seed_s
	ev_run reset make reset
	t0=$(now)
	ev_run make-up make up
	local up_ok=$?
	up_s=$(awk -v a="$t0" -v b="$(now)" 'BEGIN { printf "%.0f", b - a }')
	local up_file=$EV_OUT
	t0=$(now)
	ev_run make-seed make seed "ROWS=$ROWS" "HISTORY=$HISTORY"
	local seed_ok=$?
	seed_s=$(awk -v a="$t0" -v b="$(now)" 'BEGIN { printf "%.0f", b - a }')
	EV_CMD="make reset && make up && make seed ROWS=$ROWS HISTORY=$HISTORY"
	EV_EXPECT="cold start succeeds from empty volumes"
	EV_ACTUAL="make up ${up_s}s, make seed ${seed_s}s ($(grep -E '^ *[0-9]+ \|' "$EV_OUT" | tr -s ' ' | head -1))"
	EV_FILES="$up_file $EV_OUT"
	ev_check "$( ((up_ok == 0 && seed_ok == 0)) && echo PASS || echo FAIL)" "cold start"

	wait_for 60 "loki ready" http_ok "http://127.0.0.1:$LOKI_PORT/ready"
	EV_CMD="make check"
	EV_EXPECT="a tick for every component, no crosses"
	ev_run make-check make check
	st=$(pass_if test $? -eq 0)
	ev_png "$EV_OUT" "make check   (branch $b)"
	EV_ACTUAL="$(grep -c '✓' "$EV_OUT") ticks, $(grep -c '✗' "$EV_OUT") crosses"
	EV_FILES="$EV_OUT $EV_PNG"
	EV_SHOWS="make check output on $b, every line ticked"
	ev_check "$st" "make check"

	local key
	key=$(uuidgen 2>/dev/null || cat /proc/sys/kernel/random/uuid)
	key=$(echo "$key" | tr a-z A-Z)
	ev_run golden-path bash -c "make tx KEY=$key; make tx KEY=$key; make tx KEY=$(echo "$key" | tr A-Z a-z)"
	local ids codes
	ids=$(grep -o '"transfer_id":"[^"]*"' "$EV_OUT" | sort -u | wc -l | tr -d ' ')
	codes=$(grep -o 'HTTP [0-9]*' "$EV_OUT" | awk '{print $2}' | tr '\n' ' ')
	EV_CMD="make tx KEY=<k>; make tx KEY=<k>; make tx KEY=<k lower-cased>"
	EV_EXPECT="201 completed, then 200 twice with the same transfer_id"
	EV_ACTUAL="HTTP codes: $codes; distinct transfer_ids: $ids"
	EV_FILES="$EV_OUT"
	ev_check "$(pass_if test "$codes" = "201 200 200 " -a "$ids" = 1)" "golden path + idempotent replay"
}

# load_on: steady traffic, reset bugs.
load_on() {
	make -s bugs-off >/dev/null
	make -s load >/dev/null
}

# --- main: print debugging, no telemetry ------------------------------------

verify_main() {
	load_on
	settle 30 "traffic for logs"
	ev_run plain-logs bash -c "docker compose logs --no-log-prefix --tail=12 transfers ledger provider"
	ev_png "$EV_OUT" "docker compose logs --tail=12 transfers ledger provider   (branch main)"
	local plain json
	plain=$(grep -cE '^[0-9]{4}/[0-9]{2}/[0-9]{2} ' "$EV_OUT")
	json=$(grep -c '^{' "$EV_OUT")
	EV_CMD="make load; docker compose logs transfers ledger provider"
	EV_EXPECT="plain log.Printf lines, no JSON, no trace IDs"
	EV_ACTUAL="$plain plain lines, $json JSON lines, $(grep -c trace_id "$EV_OUT") with trace_id"
	EV_FILES="$EV_OUT $EV_PNG"
	EV_SHOWS="unstructured 'transfer trf_… acct-… -> acct-…: completed' lines with timestamps and nothing to correlate them"
	ev_check "$(pass_if test "$plain" -gt 0 -a "$json" -eq 0)" "main logs are unstructured"

	local series traces lines
	series=$(curl -sf -G "$PROM/api/v1/series" --data-urlencode 'match[]={job=~"transfers|ledger|provider"}' | jq '.data | length')
	traces=$(node_tool trace.mjs search '{resource.service.name=~"transfers|ledger|provider"}' 20 600 | jq length)
	lines=$(loki_count 'sum(count_over_time({service_name=~"transfers|ledger|provider"}[10m]))')
	ev_run no-telemetry bash -c "echo 'Prometheus series for our services: $series'; echo 'Tempo traces (10 min): $traces'; echo 'Loki lines (10 min): $lines'"
	EV_CMD="Prometheus /api/v1/series, Tempo /api/search, Loki count_over_time"
	EV_EXPECT="0 series, 0 traces, 0 log lines from our services"
	EV_ACTUAL="$series series, $traces traces, $lines lines"
	EV_FILES="$EV_OUT"
	ev_check "$(pass_if test "$series" = 0 -a "$traces" = 0 -a "${lines%.*}" = 0)" "main sends no telemetry"
	make -s load-stop >/dev/null
}

# --- 01-metrics --------------------------------------------------------------

# snapshot <label>: key numbers from Prometheus over the last minute.
snapshot() {
	local p50 p99 rps err gor cpu pool
	p50=$(p 0.5 1m)
	p99=$(p 0.99 1m)
	rps=$(prom 'sum(rate(http_server_request_duration_seconds_count{job="transfers"}[1m]))')
	err=$(prom '100 * (sum(rate(http_server_request_duration_seconds_count{job="transfers",http_response_status_code=~"5.."}[1m])) or vector(0)) / sum(rate(http_server_request_duration_seconds_count{job="transfers"}[1m]))')
	gor=$(prom 'max_over_time(go_goroutine_count{job="transfers"}[1m])')
	cpu=$(prom 'sum(rate(process_cpu_time_seconds_total{job="transfers"}[1m]))')
	pool=$(prom 'max_over_time(db_client_connection_count{db_client_connection_pool_name="ledger",db_client_connection_state="used"}[1m])')
	printf 'p50=%sms p99=%sms rps=%.1f 5xx=%.2f%% goroutines=%s transfers_cpu=%.2f ledger_pool_used=%s' \
		"$p50" "$p99" "${rps:-0}" "${err:-0}" "${gor:-?}" "${cpu:-0}" "${pool:-?}"
}
upper() { echo "$1" | tr a-z A-Z; }
num() { sed -n "s/.*$1=\([0-9.]*\).*/\1/p" <<<"$2"; }

# bug_on <a|b|c>: turn one bug on (bug A comes with a surge, it needs traffic).
SURGE_PID=""
bug_on() {
	make -s "bug-$1-on" >/dev/null
	if [[ $1 == a ]]; then
		make -s surge SURGE_SECONDS=${SURGE_SECONDS:-90} >/dev/null 2>&1 &
		SURGE_PID=$!
	fi
}
bug_off() {
	make -s bugs-off >/dev/null
	if [[ -n $SURGE_PID ]]; then
		local ids
		ids=$(docker ps -q --filter "label=com.docker.compose.project=devfest26" --filter "label=com.docker.compose.oneoff=True")
		[[ -n $ids ]] && docker stop -t 1 $ids >/dev/null 2>&1
		wait "$SURGE_PID" 2>/dev/null
		SURGE_PID=""
	fi
	# Clear any queue a saturating bug built up, then let the windows refill.
	docker compose restart transfers >/dev/null 2>&1
	wait_apps 60 >/dev/null
}

verify_01() {
	load_on
	settle 75 "a minute of healthy traffic in the rate windows"
	local healthy
	healthy=$(snapshot)
	ev_shot dashboard-healthy dashboard payments now-10m
	local audit_ok=$?
	EV_CMD="make load; open Payments — RED & USE"
	EV_EXPECT="dashboard green: p99 under the 300 ms SLO, no 5xx, every panel has data"
	EV_ACTUAL="$healthy; screenshot audit: $(jq -c .problems <<<"$EV_AUDIT")"
	EV_FILES="$EV_PNG"
	EV_SHOWS="Payments — RED & USE under steady load: green stat tiles, flat latency lines, populated USE row"
	ev_check "$(pass_if test $audit_ok -eq 0 -a "$(num p99 "$healthy")" -lt 300)" "dashboard healthy under make load"

	local use
	use=$(for q in 'go_goroutine_count' 'process_cpu_time_seconds_total' 'db_client_connection_count' 'go_memory_allocated_bytes_total' 'go_memory_used_bytes'; do
		printf '%s=%s ' "$q" "$(curl -sf -G "$PROM/api/v1/series" --data-urlencode "match[]=$q" | jq '.data | length')"
	done)
	EV_CMD="Prometheus /api/v1/series for the USE metrics"
	EV_EXPECT="every USE metric has series (goroutines, CPU, DB pool, memory)"
	EV_ACTUAL="$use"
	EV_FILES="$EV_PNG"
	EV_SHOWS="USE row: goroutines, CPU cores, DB pool used vs max, allocation rate, all with lines"
	ev_check "$(pass_if bash -c "! grep -q '=0 ' <<<'$use '")" "runtime / USE panels populated"

	local bug during st want
	for bug in c b a; do
		bug_on $bug
		settle 70 "bug $(upper $bug) on$([[ $bug == a ]] && echo ', with make surge')"
		during=$(snapshot)
		ev_shot "bug-$bug-on_dashboard" dashboard payments now-10m
		local shot_ok=$?
		case $bug in
		c)
			want="p99 > 1000 ms and goroutines at least 20 above healthy (lock queue), 5xx flat"
			st=$(pass_if awk -v p="$(num p99 "$during")" -v g="$(num goroutines "$during")" -v g0="$(num goroutines "$healthy")" -v e="$(num 5xx "$during")" 'BEGIN { exit !(p > 1000 && g >= g0 + 20 && e < 1) }')
			EV_SHOWS="bug C on: p99 tile red and climbing, goroutines rising in steps, throughput flat, 5xx still 0"
			;;
		b)
			want="p99 > 450 ms while p50 stays under 150 ms and 5xx stays under 1%; ledger DB pool busier"
			st=$(pass_if awk -v p="$(num p99 "$during")" -v m="$(num p50 "$during")" -v e="$(num 5xx "$during")" 'BEGIN { exit !(p > 450 && m < 150 && e < 1) }')
			EV_SHOWS="bug B on: p99 line lifts off while p50 stays flat, ledger p99 dominates the per-service panel, 5xx 0, ledger pool connections in use rise"
			;;
		a)
			want="with make surge: p99 > 1000 ms and transfers CPU at least 2x healthy"
			st=$(pass_if awk -v p="$(num p99 "$during")" -v c="$(num transfers_cpu "$during")" -v c0="$(num transfers_cpu "$healthy")" 'BEGIN { exit !(p > 1000 && c >= 2 * c0) }')
			EV_SHOWS="bug A plus surge: request rate up, p99 red, transfers CPU and allocation rate climbing"
			;;
		esac
		EV_CMD="make bug-$bug-on$([[ $bug == a ]] && echo ' && make surge'); wait 70 s"
		EV_EXPECT=$want
		EV_ACTUAL="before: $healthy | during: $during"
		EV_FILES="$EV_PNG"
		[[ $shot_ok -ne 0 ]] && st=FAIL && EV_ACTUAL+=" | screenshot audit: $(jq -c .problems <<<"$EV_AUDIT")"
		ev_check "$st" "bug $(upper $bug) shows on the dashboard"
		bug_off
		settle 45 "recovery"
	done
	make -s load-stop >/dev/null
}

# --- run ----------------------------------------------------------------------

info "verify: scratch clone $ROOT, evidence $RUN_DIR"
info "prebuilding images for every branch"
ev_section setup
EV_CMD="make prebuild"
EV_EXPECT="devfest26/app:<branch> built or current for every branch"
ev_run prebuild make prebuild
st=$(pass_if test $? -eq 0)
EV_ACTUAL="$(grep -c '✓' "$EV_OUT") images ready, $(grep -c '✗' "$EV_OUT") failed"
EV_FILES="$EV_OUT"
ev_check "$st" "prebuild"

for b in main 01-metrics 02-traces 03-logs 04-alerts hunt solution; do
	want "$b" || continue
	git rev-parse --verify -q "refs/heads/$b" >/dev/null || { info "no branch $b yet"; continue; }
	ev_section "$b"
	goto "$b"
	common_checks "$b"
	case $b in
	main) verify_main ;;
	01-metrics) verify_01 ;;
	02-traces) verify_02 ;;
	03-logs) verify_03 ;;
	04-alerts) verify_04 ;;
	hunt) verify_hunt ;;
	solution) verify_solution ;;
	esac
done
want stage && verify_stage
want offline && verify_offline

make -s load-stop >/dev/null 2>&1
ev_finish
status=$?
rm -rf "$SCRATCH"
exit $status
