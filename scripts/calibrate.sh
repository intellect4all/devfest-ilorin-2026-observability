#!/usr/bin/env bash
# make calibrate: measure how hard each bug hits on THIS machine, using the
# loadgen's own client-side latency (works on every branch, even main).
#
# Phases (each: bugs off, apply the fault, 15 s warm-up, 30 s measured):
#   healthy | A (steady) | A + surge | B | C
# Prints p50/p99 per phase and tells you whether to change ROWS (bug B) or
# HISTORY (bug A). Takes about 5 minutes. Leaves every bug off.
. "$(dirname "$0")/lib.sh"

RATE=${LOAD_RATE:-20}
HOT=${LOAD_HOT:-0.6}
SURGE=${SURGE_RATE:-80}
WARM=${CAL_WARM:-15}
MEASURE=${CAL_MEASURE:-30}
OUT=${CAL_OUT:-/dev/null}

T_ADMIN=http://127.0.0.1:7080/admin/bugs
L_ADMIN=http://127.0.0.1:7081/admin/bugs
bugs_off() {
	curl -sf -X POST "$T_ADMIN?a=off&c=off" >/dev/null
	curl -sf -X POST "$L_ADMIN?b=off" >/dev/null
}

loadgen() { # loadgen <rate> <seconds> <label> <hot>
	dc --profile load run --rm --no-deps -T loadgen /app/loadgen -url=http://transfers:8080/transfers \
		-rate="$1" -duration="$2s" -label="$3" -hot="$4"
}

# to_ms <go duration>: 1.234s / 85ms / 0s -> milliseconds
to_ms() {
	awk -v d="$1" 'BEGIN {
		if (d ~ /ms$/) { sub(/ms$/, "", d); print d + 0 }
		else if (d ~ /m[0-9.]+s$/) { split(d, a, "m"); sub(/s$/, "", a[2]); print a[1] * 60000 + a[2] * 1000 }
		else if (d ~ /s$/) { sub(/s$/, "", d); print d * 1000 }
		else print 0 }'
}

# summarise <label>: median of the per-window p50 and the max per-window p99
# over the measured windows (lines after the warm-up).
summarise() {
	local skip=$((WARM / 5)) p50s=() p99s=() line
	while read -r line; do
		[[ $line =~ err=([0-9]+).*p50=([^ ]+)\ p99=([^ ]+) ]] || continue
		if ((skip > 0)); then skip=$((skip - 1)); continue; fi
		p50s+=("$(to_ms "${BASH_REMATCH[2]}")")
		if ((BASH_REMATCH[1] > 0)); then
			p99s+=(10000) # requests timed out on the client: p99 is at least the timeout
		else
			p99s+=("$(to_ms "${BASH_REMATCH[3]}")")
		fi
	done
	local p50 p99
	p50=$(printf '%s\n' "${p50s[@]}" | sort -n | awk '{a[NR]=$1} END {print a[int((NR+1)/2)] + 0}')
	p99=$(printf '%s\n' "${p99s[@]}" | sort -n | tail -1)
	echo "${p50:-0} ${p99:-0}"
}

PHASES=${PHASES:-"healthy A A_surge B C"}
phase() { # phase <name> <fault command> [surge]
	local name=$1 fault=$2 surge=${3:-}
	[[ " $PHASES " == *" $name "* ]] || return 0
	bugs_off
	eval "$fault"
	local log
	log=$(mktemp)
	if [[ -n $surge ]]; then
		loadgen "$SURGE" $((WARM + MEASURE)) surge 0 >/dev/null 2>&1 &
	fi
	loadgen "$RATE" $((WARM + MEASURE)) "$name" "$HOT" >"$log" 2>&1
	wait
	local p50 p99
	read -r p50 p99 < <(summarise <"$log")
	printf -v "P50_$name" '%s' "$p50"
	printf -v "P99_$name" '%s' "$p99"
	{ echo "== $name"; cat "$log"; } >>"$OUT"
	rm -f "$log"
	printf '  %-10s p50 %6s ms   p99 %6s ms\n' "$name" "$p50" "$p99"
	bugs_off
	# A saturated phase leaves thousands of queued requests inside transfers;
	# restart it so the next phase starts clean.
	dc restart transfers >/dev/null 2>&1
	wait_apps 60 >/dev/null
}

rows=$(psql_q "SELECT count(*) FROM entries")
history=$(curl -sf "$T_ADMIN" | sed -n 's/.*"recent_transfers":\([0-9]*\).*/\1/p')
echo "calibrating on $(uname -sm), $(docker info --format '{{.NCPU}}') CPUs for Docker"
echo "  ledger entries: $rows   remembered transfers: $history   load: $RATE req/s, $HOT hot, surge +$SURGE req/s"

phase healthy ":"
phase A "curl -sf -X POST '$T_ADMIN?a=on' >/dev/null"
phase A_surge "curl -sf -X POST '$T_ADMIN?a=on' >/dev/null" surge
phase B "curl -sf -X POST '$L_ADMIN?b=on' >/dev/null"
phase C "curl -sf -X POST '$T_ADMIN?c=on' >/dev/null"

echo
echo "targets: healthy p99 < 200 ms | A steady p99 < 300 ms but A_surge > 1000 ms | B p99 600-1000 ms | C p99 > 1000 ms"
# Queueing makes bug B very non-linear near saturation, so suggest small steps.
b=${P99_B%.*}
if [[ -z $b ]]; then
	:
elif ((b < 600)); then
	echo "  bug B too mild: re-seed with about 15% more rows: make seed ROWS=$((rows * 115 / 100 / 100000 * 100000))"
elif ((b > 1000)); then
	echo "  bug B too strong: re-seed with about 10% fewer rows: make seed ROWS=$((rows * 90 / 100 / 100000 * 100000))"
else
	echo "  bug B is in range"
fi
a=${P99_A_surge%.*}
if [[ -z $a ]]; then
	:
elif ((a < 1000)); then
	echo "  bug A too mild under surge: re-seed with more history, e.g. make seed HISTORY=$((history * 3 / 2))"
elif pa=${P99_A%.*}; ((${pa:-0} > 300)); then
	echo "  bug A already hurts at steady load: re-seed with less history, e.g. make seed HISTORY=$((history * 2 / 3))"
else
	echo "  bug A is in range"
fi
