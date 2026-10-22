#!/usr/bin/env bash
# Run one test-gate leg and print its elapsed and CPU time, so a slow gate is
# attributed to a leg rather than guessed from a whole-run duration. The
# leg's own exit status is returned unchanged: a timer must never turn a
# failing leg green.
#
#   timed-leg.sh NAME COMMAND [ARG...]
#   timed-leg.sh --summary FILE START_EPOCH
#
# When GATE_TIMING_FILE is set (`make test` sets it), each leg's line is also
# appended there, and --summary prints those lines slowest first with the
# whole run's elapsed time. CPU is user+sys across every core the leg used,
# including the compiler and test binaries it waited for.

set -uo pipefail

if [ "${1:-}" = "--summary" ]; then
	[ "$#" -eq 3 ] || { echo 'timed-leg: --summary FILE START_EPOCH' >&2; exit 2; }
	echo 'gate timing, slowest leg first:'
	[ -s "$2" ] && sort -k3,3nr "$2"
	printf 'gate elapsed %ds\n' "$(($(date +%s) - $3))"
	exit 0
fi

[ "$#" -ge 2 ] || { echo 'timed-leg: NAME COMMAND [ARG...]' >&2; exit 2; }
name=$1
shift

start=$(date +%s)
"$@"
status=$?
elapsed=$(($(date +%s) - start))

# `times` must run in this shell, not a command substitution: a forked
# subshell starts with zero child CPU time.
cpu_file=$(mktemp "${TMPDIR:-/tmp}/timed-leg.XXXXXX") || exit "$status"
times >"$cpu_file"
line=$(awk -v name="$name" -v elapsed="$elapsed" -v status="$status" '
	function secs(v, parts) { sub(/s$/, "", v); split(v, parts, "m"); return parts[1] * 60 + parts[2] }
	NR == 2 { printf "leg %-24s %5ds elapsed %8.1fs user %7.1fs sys exit %d\n", name, elapsed, secs($1), secs($2), status }
' "$cpu_file")
rm -f "$cpu_file"

echo "$line"
if [ -n "${GATE_TIMING_FILE:-}" ]; then
	echo "$line" >>"$GATE_TIMING_FILE"
fi
exit "$status"
