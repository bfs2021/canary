#!/usr/bin/env bash
# The gate timer must pass a leg's exit status through unchanged, count the
# CPU of the processes the leg waited for, and summarise the recorded legs.

set -euo pipefail

script="$(cd "$(dirname "$0")" && pwd)/timed-leg.sh"
work=$(mktemp -d "${TMPDIR:-/tmp}/timed-leg-test.XXXXXX")
trap 'rm -rf "$work"' EXIT
fail() { printf 'timed-leg_test: %s\n' "$*" >&2; exit 1; }

export GATE_TIMING_FILE="$work/timing"

set +e
"$script" failing sh -c 'exit 3' >/dev/null
status=$?
set -e
[ "$status" -eq 3 ] || fail "failing leg exited $status, want 3"
grep -q '^leg failing .* exit 3$' "$GATE_TIMING_FILE" || fail 'failing leg not recorded with its status'

out=$("$script" busy sh -c 'i=0; while [ "$i" -lt 200000 ]; do i=$((i + 1)); done; echo leg-output')
printf '%s\n' "$out" | grep -qx 'leg-output' || fail 'leg output was not passed through'
user=$(awk '$2 == "busy" { sub(/s$/, "", $5); print $5 }' "$GATE_TIMING_FILE")
awk -v u="$user" 'BEGIN { exit !(u > 0) }' || fail "busy child recorded ${user}s user CPU, want > 0"

summary=$("$script" --summary "$GATE_TIMING_FILE" "$(date +%s)")
printf '%s\n' "$summary" | grep -q '^leg busy ' || fail 'summary omits a recorded leg'
printf '%s\n' "$summary" | grep -q '^gate elapsed [0-9]*s$' || fail 'summary omits the run elapsed time'

echo 'timed-leg_test: ok'
