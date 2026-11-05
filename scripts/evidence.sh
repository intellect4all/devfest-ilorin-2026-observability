# Evidence helpers for scripts/verify.sh. Source after lib.sh.
# shellcheck shell=bash
#
# A run writes into $EVIDENCE_ROOT/<UTC time>_<sha>/<branch>/NN_<name>.{txt,png}.
# Each check appends a block to the run's log.md and a row to results.tsv;
# at the end, log.md is appended to $EVIDENCE_ROOT/LOG.md (never rewritten)
# and $EVIDENCE_ROOT/SUMMARY.md is regenerated from results.tsv.

EVIDENCE_ROOT=${EVIDENCE_ROOT:-$PRIMARY/evidence}
RUN_ID="$(date -u +%Y-%m-%dT%H-%M-%SZ)_$(git -C "$PRIMARY" rev-parse --short HEAD)"
RUN_DIR="$EVIDENCE_ROOT/$RUN_ID"
mkdir -p "$RUN_DIR"
: >"$RUN_DIR/results.tsv"
printf '## Run %s\n\nHost: %s, Docker %s, %s CPUs. Started %s.\n\n' "$RUN_ID" \
	"$(uname -sm)" "$(docker version --format '{{.Server.Version}}')" "$(docker info --format '{{.NCPU}}')" \
	"$(date -u '+%Y-%m-%d %H:%M:%S UTC')" >"$RUN_DIR/log.md"

EV_BRANCH=setup
EV_N=0

# ev_section <name>: start a sub-folder (usually a branch name).
ev_section() {
	EV_BRANCH=$1
	EV_N=0
	mkdir -p "$RUN_DIR/$EV_BRANCH"
	printf '### %s\n\n' "$EV_BRANCH" >>"$RUN_DIR/log.md"
	info "== $EV_BRANCH"
}

# ev_path <name.ext>: set EV_PATH to the next numbered file in the section.
# (A global, not echo: a $(...) subshell would lose the counter.)
ev_path() {
	EV_N=$((EV_N + 1))
	EV_PATH=$(printf '%s/%s/%02d_%s' "$RUN_DIR" "$EV_BRANCH" "$EV_N" "$1")
}

rel() { echo "${1#"$EVIDENCE_ROOT"/}"; }

# ev_run <name> <command...>: run a command, keep its full output. Sets EV_OUT.
ev_run() {
	local name=$1
	shift
	ev_path "$name.txt"
	EV_OUT=$EV_PATH
	{
		printf '$ %s\n' "$*"
		printf '# %s, branch %s @ %s\n' "$(date -u '+%Y-%m-%d %H:%M:%S UTC')" "$(git rev-parse --abbrev-ref HEAD)" "$(git rev-parse --short HEAD)"
		"$@" 2>&1
		printf '# exit %s\n' "$?"
	} >"$EV_OUT"
	tail -1 "$EV_OUT" | grep -q '^# exit 0$'
}

# ev_png <txt> [title]: render a text file as a terminal-style PNG next to it.
ev_png() {
	local png=${1%.txt}.png
	SHOOT_OUT_DIR="$EVIDENCE_ROOT" scripts/shoot.sh text "$1" "$png" "${2:-$(head -1 "$1" | sed 's/^\$ //')}" >/dev/null
	EV_PNG=$png
}

# ev_shot <name> <shoot.mjs command> <args...>: screenshot into the next file.
# The output path is inserted as the second argument. Sets EV_PNG, EV_AUDIT.
ev_shot() {
	local name=$1 cmd=$2
	shift 2
	ev_path "$name.png"
	EV_PNG=$EV_PATH
	local first=$1
	shift
	EV_AUDIT=$(SHOOT_OUT_DIR="$EVIDENCE_ROOT" scripts/shoot.sh "$cmd" "$first" "$EV_PNG" "$@" 2>&1 | tail -1)
	printf '%s\n' "$EV_AUDIT" >"${EV_PNG%.png}.json"
	[[ $EV_AUDIT == *'"problems":[]'* ]]
}

# ev_check <PASS|FAIL> <title>, with these set beforehand:
#   EV_CMD     command(s) run          EV_EXPECT  what should happen
#   EV_ACTUAL  what happened (numbers) EV_SHOWS   what the screenshot shows
#   EV_FILES   space-separated proof files (absolute paths)
ev_check() {
	local status=$1 title=$2 links="" f
	for f in ${EV_FILES:-}; do links+="[$(basename "$f")]($RUN_ID/$(rel "$f" | sed "s|^$RUN_ID/||")) "; done
	{
		printf -- '- **%s** %s  \n' "$status" "$title"
		printf '  %s · branch `%s` @ `%s`  \n' "$(date -u '+%Y-%m-%d %H:%M:%S UTC')" "$(git rev-parse --abbrev-ref HEAD)" "$(git rev-parse --short HEAD)"
		printf '  commands: `%s`  \n' "${EV_CMD:-}"
		printf '  expected: %s  \n' "${EV_EXPECT:-}"
		printf '  actual: %s  \n' "${EV_ACTUAL:-}"
		[[ -n ${EV_SHOWS:-} ]] && printf '  screenshot shows: %s  \n' "$EV_SHOWS"
		printf '  proof: %s\n\n' "${links:-none}"
	} >>"$RUN_DIR/log.md"
	printf '%s\t%s\t%s\t%s\t%s\n' "$EV_BRANCH" "$status" "$title" "${EV_ACTUAL:-}" "${links:-}" >>"$RUN_DIR/results.tsv"
	if [[ $status == PASS ]]; then ok "$title"; else bad "$title: ${EV_ACTUAL:-}"; fi
	EV_CMD="" EV_EXPECT="" EV_ACTUAL="" EV_SHOWS="" EV_FILES=""
}

# pass_if <condition command...>: PASS when the command succeeds, else FAIL.
pass_if() { if "$@"; then echo PASS; else echo FAIL; fi; }

# ev_finish: append the run log to LOG.md and regenerate SUMMARY.md.
ev_finish() {
	local pass fail
	pass=$(awk -F'\t' '$2=="PASS"' "$RUN_DIR/results.tsv" | wc -l | tr -d ' ')
	fail=$(awk -F'\t' '$2=="FAIL"' "$RUN_DIR/results.tsv" | wc -l | tr -d ' ')
	printf '**Run %s finished %s: %s PASS, %s FAIL.**\n\n---\n\n' "$RUN_ID" "$(date -u '+%H:%M:%S UTC')" "$pass" "$fail" >>"$RUN_DIR/log.md"
	[[ -f $EVIDENCE_ROOT/LOG.md ]] || printf '# Evidence log\n\nAppend-only history of every verification run, failures included. Newest at the bottom.\n\n' >"$EVIDENCE_ROOT/LOG.md"
	cat "$RUN_DIR/log.md" >>"$EVIDENCE_ROOT/LOG.md"
	{
		printf '# Evidence summary\n\nLatest run: **%s**: %s PASS, %s FAIL. Full history in [LOG.md](LOG.md).\n\n' "$RUN_ID" "$pass" "$fail"
		[[ -f $RUN_DIR/notes.md ]] && cat "$RUN_DIR/notes.md"
		printf '| Section | Result | Check | Measured | Proof |\n|---|---|---|---|---|\n'
		awk -F'\t' '{ gsub(/\|/, "\\|", $4); printf "| %s | %s | %s | %s | %s |\n", $1, ($2=="PASS" ? "✅ PASS" : "❌ FAIL"), $3, $4, $5 }' "$RUN_DIR/results.tsv"
	} >"$EVIDENCE_ROOT/SUMMARY.md"
	info "evidence: $RUN_DIR ($pass PASS, $fail FAIL)"
	((fail == 0))
}

# --- Queries against the stack ------------------------------------------------

# prom <promql>: instant value (empty if no data).
prom() {
	curl -sf -G "$PROM/api/v1/query" --data-urlencode "query=$1" | jq -r '.data.result[0].value[1] // empty'
}

# ms <seconds>: seconds (possibly empty or NaN) to whole milliseconds.
ms() { awk -v v="${1:-0}" 'BEGIN { if (v == "NaN" || v == "") print "n/a"; else printf "%d\n", v * 1000 }'; }

# p <quantile> [window]: transfers server-side latency quantile, in ms.
p() {
	ms "$(prom "histogram_quantile($1, sum by (le) (rate(http_server_request_duration_seconds_bucket{job=\"transfers\"}[${2:-1m}])))")"
}

# loki_count <logql metric query>: instant scalar.
loki_count() {
	curl -sf -G "http://127.0.0.1:$LOKI_PORT/loki/api/v1/query" --data-urlencode "query=$1" | jq -r '[.data.result[].value[1] | tonumber] | add // 0'
}

# node_tool <script> <args...>: run a node script from tools/evidence in the
# Playwright image (host network), e.g. trace.mjs.
node_tool() {
	docker run --rm --network host --user "$(id -u):$(id -g)" -e HOME=/tmp \
		-e TEMPO_URL="http://127.0.0.1:$TEMPO_PORT" -v "$ROOT/tools/evidence":/w:ro -w /w \
		mcr.microsoft.com/playwright:v1.63.0-noble node "/w/$1" "${@:2}"
}
