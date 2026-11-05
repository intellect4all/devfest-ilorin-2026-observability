# Checks for 02-traces onwards, the stage cycle and the offline kit.
# Sourced by scripts/verify.sh (uses its helpers: ev_*, snapshot, bug_on, ...).
# shellcheck shell=bash

# Which span should carry the time, per bug: the "where" of each incident.
expected_span() {
	case $1 in
	a) echo duplicate_check ;;
	b) echo "SELECT entries" ;;
	c) echo account_lock.wait ;;
	esac
}
# And which log message explains it: the "why".
expected_log() {
	case $1 in
	a) echo "slow duplicate check" ;;
	b) echo "slow query" ;;
	c) echo "waited for account lock" ;;
	esac
}

# The Loki fields that carry the numbers, ticked in the Fields sidebar so
# they show on the line (the same click path the runbook gives).
log_view() {
	case $1 in
	a) echo "slow duplicate check|Log line,entries_scanned,duration_ms" ;;
	b) echo "slow query|Log line,duration_ms,db_query_text" ;;
	c) echo "waited for|Log line,wait_ms,held_by_transfer,held_by_step" ;;
	esac
}

SLOW_TRANSFERS='{resource.service.name="transfers" && name="POST /transfers"}'

# slowest_trace [seconds]: trace ID of the slowest recent transfer.
slowest_trace() {
	node_tool trace.mjs search "$SLOW_TRANSFERS" 100 "${1:-60}" | jq -r '.[0].traceID // empty'
}

# tx_trace: send one transfer, print "<transfer_id> <trace_id>".
tx_trace() {
	local key out tid trace=""
	key=$(uuidgen 2>/dev/null || cat /proc/sys/kernel/random/uuid)
	out=$(make -s tx KEY="$key")
	tid=$(grep -o '"transfer_id":"[^"]*"' <<<"$out" | cut -d'"' -f4)
	for _ in 1 2 3 4 5 6 7 8 9 10; do
		sleep 2
		trace=$(node_tool trace.mjs search "{span.transfer.id=\"$tid\"}" 5 300 | jq -r '.[0].traceID // empty')
		[[ -n $trace ]] && break
	done
	echo "$tid $trace"
}

# --- 02-traces ---------------------------------------------------------------

verify_02() {
	load_on
	settle 30 "traffic"

	local tid trace analysis services roots orphans
	read -r tid trace < <(tx_trace)
	ev_run one-trace node_tool trace.mjs analyze "$trace"
	analysis=$(grep '^{' "$EV_OUT" | head -1)
	services=$(jq -r '.services | join(",")' <<<"$analysis")
	roots=$(jq -r '.roots | length' <<<"$analysis")
	local trace_file=$EV_OUT
	orphans=$(node_tool trace.mjs search '{trace:rootService != "transfers"}' 100 300 | jq length)
	ev_shot one-transfer-one-trace trace "$trace"
	local shot_ok=$?
	EV_CMD="make tx; Tempo search {span.transfer.id=\"$tid\"}; Tempo search {trace:rootService != \"transfers\"} over 5 min"
	EV_EXPECT="one trace with spans from transfers, ledger and provider, a single root; no trace rooted anywhere else"
	EV_ACTUAL="trace $trace: services [$services], roots $roots, $(jq .spanCount <<<"$analysis") spans; traces with another root in the last 5 min: $orphans"
	EV_FILES="$trace_file $EV_PNG"
	EV_SHOWS="Tempo trace view of one POST /transfers: transfers → ledger GET /balance or POST /entries → SELECT/INSERT entries, and provider POST /payouts, nested in one waterfall"
	ev_check "$(pass_if test "$services" = "ledger,provider,transfers" -a "$roots" = 1 -a "$orphans" = 0 -a $shot_ok -eq 0)" "one transfer = one trace across 3 services, no orphans"

	local bug want got slow
	for bug in c b a; do
		bug_on $bug
		settle 50 "bug $(upper $bug) on"
		slow=$(slowest_trace 40)
		ev_run "bug-$bug-slowest-trace" node_tool trace.mjs analyze "$slow"
		analysis=$(grep '^{' "$EV_OUT" | head -1)
		trace_file=$EV_OUT
		want=$(expected_span $bug)
		got=$(jq -r '.widest.name' <<<"$analysis")
		ev_shot "bug-$bug-trace" trace "$slow" "$got"
		shot_ok=$?
		local extra=""
		[[ $bug == a ]] && extra="; duplicate_check self/total ms: $(jq -r '[.spans[] | select(.name=="duplicate_check")][0] | "\(.selfMs)/\(.durMs)"' <<<"$analysis")"
		EV_CMD="make bug-$bug-on$([[ $bug == a ]] && echo ' + make surge'); slowest transfer trace of the last 40 s"
		EV_EXPECT="the span with the most self time is '$want'"
		EV_ACTUAL="trace $slow ($(jq -r '[.spans[0].durMs] | .[0]' <<<"$analysis") ms): widest '$got' $(jq -r '.widest | "\(.selfMs) ms self in \(.service)"' <<<"$analysis")$extra"
		EV_FILES="$trace_file $EV_PNG"
		case $bug in
		a) EV_SHOWS="slow transfer trace: a long duplicate_check bar with nothing under it, everything after it normal" ;;
		b) EV_SHOWS="slow transfer trace: balance_check → GET /balance → SELECT entries is the long bar; span details show db.query.text" ;;
		c) EV_SHOWS="slow transfer trace: a long account_lock.wait bar before any real work; post_entries and payout are short" ;;
		esac
		local st
		st=$(pass_if test "$got" = "$want" -a $shot_ok -eq 0)
		[[ $bug == a ]] && [[ $(jq '[.spans[] | select(.name=="duplicate_check")][0] | (.durMs - .selfMs) < 1' <<<"$analysis") != true ]] && st=FAIL
		ev_check "$st" "bug $(upper $bug): slowest trace points at $want"
		bug_off
		settle 30 "recovery"
	done
	make -s load-stop >/dev/null
}

# --- 03-logs -----------------------------------------------------------------

# request-path lines without a trace_id, per service, in the last N minutes.
# Start-up lines (before any request) have no trace and are excluded.
STARTUP='!~ "listening on|applied migration|loaded [0-9]+ recent|telemetry"'

verify_03() {
	load_on
	settle 30 "traffic"

	# Bug C makes transfers WARN, bug B makes ledger WARN, a flaky provider
	# makes provider WARN: log lines from every service for the next check.
	local start=$SECONDS
	bug_on c
	settle 45 "bug C on: WARN lines"
	local slow
	slow=$(slowest_trace 40)
	ev_shot span-to-logs trace "$slow" account_lock.wait logs "$(log_view c)"
	local shot_ok=$?
	local logs_png=$EV_PNG
	EV_CMD="slowest trace with bug C on → span account_lock.wait → Logs for this span"
	EV_EXPECT="the Loki pane opens with this request's lines, including the WARN"
	EV_ACTUAL="trace $slow; audit $(jq -c .problems <<<"$EV_AUDIT")"
	EV_FILES="$logs_png"
	EV_SHOWS="split view: the trace on the left with account_lock.wait open; Loki on the right (Related logs), filtered to this trace_id, the WARN 'waited for account lock' line showing wait_ms, held_by_transfer and held_by_step=payout"
	ev_check "$(pass_if test $shot_ok -eq 0)" "span → logs jump works"
	bug_off
	bug_on b
	settle 30 "bug B on: ledger WARN lines"
	bug_off
	make -s provider-flaky >/dev/null
	settle 20 "provider rejecting 20%: provider WARN, transfers ERROR"
	make -s provider-normal >/dev/null
	settle 5 "flush"

	local mins=$(((SECONDS - start) / 60 + 1)) svc total missing summary="" st=PASS
	for svc in transfers ledger provider; do
		total=$(loki_count "sum(count_over_time({service_name=\"$svc\"} $STARTUP [${mins}m]))")
		missing=$(loki_count "sum(count_over_time({service_name=\"$svc\"} $STARTUP | trace_id=\"\" [${mins}m]))")
		summary+="$svc: ${total%.*} lines, ${missing%.*} without trace_id; "
		[[ ${total%.*} -gt 0 && ${missing%.*} -eq 0 ]] || st=FAIL
	done
	ev_run stdout-json bash -c "docker compose logs --no-log-prefix --since ${mins}m transfers ledger provider | grep -E '\"level\":\"(WARN|ERROR)\"' | tail -6"
	local stdout_file=$EV_OUT
	ev_png "$EV_OUT" "docker compose logs: WARN/ERROR lines, JSON with trace_id   (branch 03-logs)"
	grep '^{' "$stdout_file" | grep -qv trace_id && st=FAIL
	ev_shot loki-warn-errors loki '{service_name=~"transfers|ledger|provider"} | detected_level=~"warn|error"' "now-${mins}m"
	local loki_png=$EV_PNG
	EV_CMD="Loki count_over_time per service, with and without trace_id (start-up lines excluded); docker compose logs"
	EV_EXPECT="every service has request log lines in Loki and none lacks a trace_id; stdout lines are JSON with trace_id"
	EV_ACTUAL="$summary"
	EV_FILES="$stdout_file $EV_PNG $loki_png"
	EV_SHOWS="terminal: JSON WARN/ERROR lines from all three services, each with trace_id and span_id; Loki: the same lines in Explore"
	ev_check "$st" "every service's log lines carry trace_id"

	local bug want trace found lines
	for bug in c b a; do
		bug_on $bug
		settle 50 "bug $(upper $bug) on"
		trace=$(slowest_trace 40)
		want=$(expected_log $bug)
		settle 5 "logs flushed"
		ev_run "bug-$bug-logs-of-slowest-trace" bash -c "curl -sf -G 'http://127.0.0.1:$LOKI_PORT/loki/api/v1/query_range' --data-urlencode 'query={service_name=~\"transfers|ledger|provider\"} | trace_id=\"$trace\"' --data-urlencode start=$(($(date +%s) - 300))000000000 | jq -c '.data.result[] | {svc: .stream.service_name, level: .stream.detected_level, line: .values[][1], fields: (.stream | del(.service_name, .detected_level, .observed_timestamp, .flags, .scope_name, .telemetry_sdk_language, .telemetry_sdk_name, .telemetry_sdk_version, .severity_number))}'"
		found=$(grep -c "$want" "$EV_OUT")
		lines=$EV_OUT
		ev_shot "bug-$bug-trace-to-logs" trace "$trace" "$(expected_span $bug)" logs "$(log_view $bug)"
		shot_ok=$?
		EV_CMD="make bug-$bug-on; slowest trace → Logs for this span"
		EV_EXPECT="the trace's logs contain '$want' with the numbers that explain it"
		EV_ACTUAL="trace $trace: $found matching line(s): $(grep "$want" "$EV_OUT" | head -1 | jq -c '.fields | del(.trace_id, .span_id)' 2>/dev/null)"
		EV_FILES="$lines $EV_PNG"
		case $bug in
		a) EV_SHOWS="trace plus its logs: 'slow duplicate check' with entries_scanned in the tens of thousands and duration_ms" ;;
		b) EV_SHOWS="trace plus its logs: ledger 'slow query' with duration_ms and db.query.text" ;;
		c) EV_SHOWS="trace plus its logs: 'waited for account lock' with wait_ms, held_by_transfer and held_by_step=payout" ;;
		esac
		ev_check "$(pass_if test "$found" -gt 0 -a $shot_ok -eq 0)" "bug $(upper $bug): the trace's logs say why ($want)"
		bug_off
		settle 30 "recovery"
	done
	make -s load-stop >/dev/null
}

# --- 04-alerts ---------------------------------------------------------------

# ntfy_since <epoch> <grep pattern>: first message title matching, as "time title".
ntfy_since() {
	local base=${NTFY_URL%/}
	[[ $base == http://ntfy ]] && base="http://127.0.0.1:$NTFY_LOCAL_PORT"
	curl -sf "$base/$NTFY_TOPIC/json?poll=1&since=$1" | jq -r 'select(.event=="message") | "\(.time) \(.title)"' | grep -E "$2" | head -1
}

ntfy_web() {
	local base=${NTFY_URL%/}
	[[ $base == http://ntfy ]] && base="http://127.0.0.1:$NTFY_LOCAL_PORT"
	echo "$base/$NTFY_TOPIC"
}

# wait_ntfy <epoch> <pattern> <timeout>: print the matching message's time.
wait_ntfy() {
	local deadline=$((SECONDS + $3)) m
	while ((SECONDS < deadline)); do
		m=$(ntfy_since "$1" "$2")
		[[ -n $m ]] && { echo "$m"; return 0; }
		sleep 2
	done
	return 1
}

verify_04() {
	load_on
	settle 45 "healthy traffic"

	local t0 m
	t0=$(date +%s)
	ev_run alert-test make alert-test
	m=$(wait_ntfy "$t0" "Test alert" 30)
	ev_shot ntfy-test page "$(ntfy_web)" "Test alert from make alert-test"
	EV_CMD="make alert-test"
	EV_EXPECT="a readable sample notification arrives on the ntfy topic"
	EV_ACTUAL="ntfy: ${m:-nothing within 30 s}"
	EV_FILES="$EV_OUT $EV_PNG"
	EV_SHOWS="ntfy web app on the topic with the 'Test alert from make alert-test' notification"
	ev_check "$(pass_if test -n "$m")" "make alert-test reaches ntfy"

	t0=$(date +%s)
	bug_on c
	m=$(wait_ntfy "$t0" "^[0-9]+ Transfers p99" 240)
	local t_fire=${m%% *}
	ev_shot dashboard-firing dashboard payments now-10m
	local dash=$EV_PNG
	ev_shot ntfy-firing page "$(ntfy_web)" "Transfers p99 latency above 300 ms"
	local shot_ok=$?
	EV_CMD="make bug-c-on (the main incident), then watch the ntfy topic"
	EV_EXPECT="'Transfers p99 latency above 300 ms' arrives as a readable notification (title, value, time), not JSON"
	EV_ACTUAL="ntfy: ${m:-nothing within 240 s}; bug-on to notification: $((${t_fire:-0} - t0)) s"
	EV_FILES="$dash $EV_PNG"
	EV_SHOWS="ntfy web app: urgent 'Transfers p99 latency above 300 ms' with the p99 value and the time it started; dashboard: alert panel firing"
	ev_check "$(pass_if test -n "$m" -a $shot_ok -eq 0)" "main incident alert fires and reaches ntfy"
	echo "alert_latency_fire_s=$((${t_fire:-0} - t0))" >>"$RUN_DIR/numbers.txt"

	t0=$(date +%s)
	bug_off
	m=$(wait_ntfy "$t0" "Resolved: Transfers p99" 240)
	local t_res=${m%% *}
	ev_shot ntfy-resolved page "$(ntfy_web)" "Resolved: Transfers p99 latency above 300 ms"
	shot_ok=$?
	EV_CMD="make bugs-off, then watch the ntfy topic"
	EV_EXPECT="a 'Resolved:' notification arrives"
	EV_ACTUAL="ntfy: ${m:-nothing within 240 s}; bug-off to resolved: $((${t_res:-0} - t0)) s"
	EV_FILES="$EV_PNG"
	EV_SHOWS="ntfy web app: 'Resolved: Transfers p99 latency above 300 ms' under the firing one"
	ev_check "$(pass_if test -n "$m" -a $shot_ok -eq 0)" "alert resolves and the resolve reaches ntfy"
	echo "alert_latency_resolve_s=$((${t_res:-0} - t0))" >>"$RUN_DIR/numbers.txt"

	t0=$(date +%s)
	make -s provider-flaky >/dev/null
	m=$(wait_ntfy "$t0" "^[0-9]+ Transfers 5xx" 180)
	make -s provider-normal >/dev/null
	EV_CMD="make provider-flaky"
	EV_EXPECT="'Transfers 5xx above 5%' fires and reaches ntfy"
	EV_ACTUAL="ntfy: ${m:-nothing within 180 s}; provider-flaky to notification: $((${m%% *} - t0)) s"
	EV_FILES=""
	ev_check "$(pass_if test -n "$m")" "5xx alert fires on a flaky provider"
	wait_ntfy "$t0" "Resolved: Transfers 5xx" 180 >/dev/null
	make -s load-stop >/dev/null
}

# --- hunt --------------------------------------------------------------------

# A walkthrough that uses only what HUNT.md tells attendees to look at:
# dashboard → slowest trace → its widest span → logs for that span.
verify_hunt() {
	load_on
	settle 30 "traffic"
	local bug verdict answer trace analysis widest logs found guess
	for bug in a b c; do
		ev_run "mystery-$bug" make mystery BUG=$bug
		settle 60 "the mystery bug is live"
		ev_shot "hunt-$bug-1-dashboard" dashboard payments now-10m
		local dash=$EV_PNG
		trace=$(slowest_trace 40)
		analysis=$(node_tool trace.mjs analyze "$trace")
		widest=$(jq -r '.widest.name' <<<"$analysis")
		ev_shot "hunt-$bug-2-trace" trace "$trace" "$widest"
		local tr_png=$EV_PNG
		local view=""
		case $widest in duplicate_check) view=$(log_view a) ;; "SELECT entries") view=$(log_view b) ;; account_lock.wait) view=$(log_view c) ;; esac
		ev_shot "hunt-$bug-3-logs" trace "$trace" "$widest" logs "$view"
		local logs_png=$EV_PNG
		logs=$(curl -sf -G "http://127.0.0.1:$LOKI_PORT/loki/api/v1/query_range" \
			--data-urlencode "query={service_name=~\"transfers|ledger|provider\"} | trace_id=\"$trace\" | detected_level=\"warn\"" \
			--data-urlencode "start=$(($(date +%s) - 300))000000000" | jq -r '.data.result[].values[][1]' | sort -u | tr '\n' ';')
		case "$widest|$logs" in
		duplicate_check*"slow duplicate check"*) guess=a ;;
		"SELECT entries"*"slow query"*) guess=b ;;
		account_lock.wait*"waited for account lock"*) guess=c ;;
		*) guess="?" ;;
		esac
		ev_run "reveal-$bug" make reveal
		answer=$(grep -o 'Bug [ABC]' "$EV_OUT" | head -1)
		EV_CMD="make mystery (secret: $bug); dashboard; slowest trace; widest span; Logs for this span; make reveal"
		EV_EXPECT="following HUNT.md only, the signals name the bug that make reveal confirms"
		EV_ACTUAL="widest span '$widest', WARN line(s) '$logs' → guessed bug $(upper "$guess"); make reveal: $answer"
		EV_FILES="$dash $tr_png $logs_png $EV_OUT"
		EV_SHOWS="1 dashboard with the symptom, 2 the slowest trace with the widest span open, 3 that span's logs with the WARN line that names the cause"
		ev_check "$(pass_if test "Bug $(upper "$guess")" = "$answer")" "hunt walkthrough finds bug $(upper $bug)"
		settle 30 "recovery"
	done
	make -s load-stop >/dev/null
}

# --- solution ----------------------------------------------------------------

verify_solution() {
	load_on
	settle 45 "healthy traffic"
	local before during
	before=$(snapshot)
	make -s bug-a-on bug-b-on bug-c-on >/dev/null
	make -s surge SURGE_SECONDS=100 >/dev/null 2>&1 &
	SURGE_PID=$!
	settle 75 "every bug toggle on, plus a surge"
	during=$(snapshot)
	ev_shot all-bugs-on_dashboard dashboard payments now-10m
	local shot_ok=$?
	local dash=$EV_PNG
	ev_run explain make explain
	ev_png "$EV_OUT" "make explain with bug B's toggle on   (branch solution)"
	EV_CMD="make bug-a-on bug-b-on bug-c-on; make surge; make explain"
	EV_EXPECT="p99 stays under the 300 ms SLO with every toggle on and a surge; the balance query uses an index"
	EV_ACTUAL="before: $before | all toggles on + surge: $during | plan: $(grep -oE 'Index Only Scan|Index Scan|Seq Scan' "$EV_OUT" | head -1)"
	EV_FILES="$dash $EV_OUT $EV_PNG"
	EV_SHOWS="dashboard still green under a surge with all toggles on; EXPLAIN shows an Index Only Scan on idx_entries_account_balance"
	ev_check "$(pass_if test "$(num p99 "$during")" -lt 300 -a $shot_ok -eq 0 -a "$(grep -c 'Seq Scan' "$EV_OUT")" -eq 0)" "fixes hold with every bug toggle on"
	bug_off
	make -s load-stop >/dev/null
}

# --- stage switching -----------------------------------------------------------

verify_stage() {
	ev_section stage
	goto main
	make -s reset >/dev/null 2>&1
	make -s up >/dev/null 2>&1
	make -s seed ROWS="$ROWS" HISTORY="$HISTORY" >/dev/null 2>&1
	load_on
	settle 20 "traffic"
	local b secs all="" st
	for b in 01-metrics 02-traces 03-logs hunt 04-alerts solution main; do
		ev_run "stage-$b" env STAGE_TIME_FILE="$RUN_DIR/stage-$b.secs" make stage STAGE=$b
		st=$(pass_if test $? -eq 0)
		secs=$(cat "$RUN_DIR/stage-$b.secs" 2>/dev/null || echo 99)
		local out=$EV_OUT
		grep -qE 'Building|go build|\[build' "$out" && st=FAIL
		all+="$b ${secs}s, "
		settle 25 "metrics flowing on $b"
		if [[ $b == main ]]; then
			ev_run "stage-$b-check" make check
			ev_png "$EV_OUT" "make check after make stage STAGE=main"
			[[ $(grep -c '✗' "$EV_OUT") -eq 0 ]] || st=FAIL
		else
			ev_shot "stage-$b-dashboard" dashboard payments now-5m || st=FAIL
		fi
		awk -v s="$secs" 'BEGIN { exit !(s < 15) }' || st=FAIL
		EV_CMD="make stage STAGE=$b (load running)"
		EV_EXPECT="switch in under 15 s, no compile, lands on a working dashboard"
		EV_ACTUAL="switched in ${secs}s; $(tail -2 "$out" | head -1)"
		EV_FILES="$out $EV_PNG"
		EV_SHOWS="$([[ $b == main ]] && echo "make check all ticks after switching back to main" || echo "Payments dashboard on $b, panels drawing, data continuing across the switch")"
		ev_check "$st" "stage switch to $b"
	done
	echo "stage_switch_seconds=${all%, }" >>"$RUN_DIR/numbers.txt"
	make -s load-stop >/dev/null
}

# --- offline kit -------------------------------------------------------------

verify_offline() {
	ev_section offline
	goto main
	ev_run offline-kit make offline-kit
	local kit_ok=$?
	local kit_file=$EV_OUT
	local kit="$ROOT/offline-kit"
	EV_CMD="make offline-kit"
	EV_EXPECT="images tarball, git bundle, vendored modules and instructions written"
	EV_ACTUAL="$(du -sh "$kit" 2>/dev/null | cut -f1) kit: $(ls "$kit" 2>/dev/null | tr '\n' ' ')"
	EV_FILES="$kit_file"
	ev_check "$(pass_if test $kit_ok -eq 0 -a -f "$kit/images.tar")" "make offline-kit"

	# A clean Docker with no route to any registry: docker:dind on --network none.
	make -s down >/dev/null 2>&1
	ev_run offline-test scripts/offline-test.sh "$kit"
	local st
	st=$(pass_if test $? -eq 0)
	ev_png "$EV_OUT" "offline test: docker load + make up + make check, no network"
	EV_CMD="scripts/offline-test.sh: docker:dind --network none; docker load; make up; make seed; make check"
	EV_EXPECT="the stack starts and make check is all ticks with no registry reachable (local ntfy as Plan B)"
	EV_ACTUAL="$(grep -E 'pull|registry|✗|✓ all good|images loaded' "$EV_OUT" | tail -4 | tr '\n' ' ')"
	EV_FILES="$EV_OUT $EV_PNG"
	EV_SHOWS="inside an offline Docker: images loaded from the tarball, make check all ticks with ntfy pointing at the local container"
	ev_check "$st" "offline kit works with registries blocked"
}
