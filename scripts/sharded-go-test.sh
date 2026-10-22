#!/usr/bin/env bash
# Run one Go package's tests under -race, split across several processes.
#
#   sharded-go-test.sh [-tags TAGS] [-skip-file FILE] [-shards N] [-timeout D] PACKAGE
#
# `go test` runs a package's non-parallel tests one after another in one
# process; for the daemon root that serial chain was nearly all of its
# wall-clock time. Separate processes isolate exactly the process-wide state
# that keeps those tests serial, so the package binary is compiled once and
# its test list is dealt round-robin to N processes. Each process gets its own
# HOME and XDG directories, so shards cannot share files with each other or
# with the operator's own Canary state.
#
# The run is accepted only when every listed test, minus the skip file,
# reported exactly one top-level PASS or SKIP, and no test failed. A skip file
# names tests one per line; a name the binary does not list is an error, so a
# stale skip list cannot hide anything.

set -euo pipefail

tags=
skip_file=
shards=6
timeout=600s
while [ "$#" -gt 1 ]; do
	case "$1" in
	-tags) tags=$2; shift 2 ;;
	-skip-file) skip_file=$2; shift 2 ;;
	-shards) shards=$2; shift 2 ;;
	-timeout) timeout=$2; shift 2 ;;
	*) break ;;
	esac
done
[ "$#" -eq 1 ] || { echo 'usage: sharded-go-test.sh [-tags TAGS] [-skip-file FILE] [-shards N] [-timeout D] PACKAGE' >&2; exit 2; }
[[ "$shards" =~ ^[1-9][0-9]?$ ]] || { echo "sharded-go-test: -shards must be 1-99, got $shards" >&2; exit 2; }
pkg=$1
tag_flag=()
[ -n "$tags" ] && tag_flag=(-tags "$tags")

dir=$(go list ${tag_flag[@]+"${tag_flag[@]}"} -f '{{.Dir}}' "$pkg")
import=$(go list ${tag_flag[@]+"${tag_flag[@]}"} "$pkg")
work=$(mktemp -d "${TMPDIR:-/tmp}/sharded-go-test.XXXXXX")
trap 'rm -rf "$work"' EXIT HUP INT TERM

go test -c -race ${tag_flag[@]+"${tag_flag[@]}"} -o "$work/pkg.test" "$pkg"
(cd "$dir" && "$work/pkg.test" -test.list '.*') | grep -E '^(Test|Fuzz|Example)' | sort -u >"$work/listed"

cp "$work/listed" "$work/run"
if [ -n "$skip_file" ]; then
	sort -u "$skip_file" | grep -v '^$' >"$work/skip" || true
	unknown=$(grep -Fxv -f "$work/listed" "$work/skip" || true)
	if [ -n "$unknown" ]; then
		echo "sharded-go-test: skip file names tests $import does not list:" >&2
		printf '  %s\n' $unknown >&2
		exit 1
	fi
	if [ -s "$work/skip" ]; then
		grep -Fxv -f "$work/skip" "$work/listed" >"$work/run" || true
	fi
fi
total=$(wc -l <"$work/run" | tr -d ' ')
[ "$total" -gt 0 ] || { echo "sharded-go-test: no tests to run in $import" >&2; exit 1; }

awk -v n="$shards" -v w="$work" '{ print > (w "/shard." ((NR - 1) % n)) }' "$work/run"

start=$(date +%s)
pids=()
for list in "$work"/shard.*; do
	i=${list##*.}
	home="$work/home.$i"
	mkdir -p "$home/state" "$home/cache" "$home/config" "$home/data"
	(
		cd "$dir"
		HOME="$home" XDG_STATE_HOME="$home/state" XDG_CACHE_HOME="$home/cache" \
			XDG_CONFIG_HOME="$home/config" XDG_DATA_HOME="$home/data" \
			"$work/pkg.test" -test.v -test.paniconexit0 -test.timeout="$timeout" \
			-test.run "^($(paste -sd'|' "$list"))\$"
	) >"$work/log.$i" 2>&1 &
	pids+=("$!")
done

failed=0
for pid in "${pids[@]}"; do
	wait "$pid" || failed=1
done

# Top-level results are the unindented `--- PASS|SKIP|FAIL: Name (` lines.
cat "$work"/log.* | awk '/^--- (PASS|SKIP|FAIL): / { print $2, $3 }' | tr -d ':' | sort >"$work/results"
fails=$(awk '$1 == "FAIL" { print $2 }' "$work/results")
awk '$1 != "FAIL" { print $2 }' "$work/results" | sort >"$work/reported"
missing=$(printf '%s\n' $fails | cat - "$work/reported" | sort | comm -23 "$work/run" -)
extra=$(sort "$work/reported" | uniq -d; comm -13 "$work/run" "$work/reported")

if [ "$failed" -ne 0 ] || [ -n "$fails" ] || [ -n "$missing" ] || [ -n "$extra" ]; then
	for log in "$work"/log.*; do
		if grep -qE '^(--- FAIL|FAIL|panic:)' "$log"; then
			echo "=== $import shard ${log##*.} log"
			grep -vE '^(=== (RUN|PAUSE|CONT|NAME)|--- PASS|    --- PASS)' "$log"
		fi
	done
	[ -n "$fails" ] && { echo "sharded-go-test: failed:"; printf '  %s\n' $fails; }
	[ -n "$missing" ] && { echo "sharded-go-test: listed but never reported (crash, exit or timeout):"; printf '  %s\n' $missing; }
	[ -n "$extra" ] && { echo "sharded-go-test: reported twice or not listed:"; printf '  %s\n' $extra; }
	echo "FAIL	$import	$shards shards"
	exit 1
fi
skipped=$(( $(wc -l <"$work/listed") - total ))
printf 'ok  \t%s\t%ds\t%d tests in %d shards%s\n' "$import" "$(($(date +%s) - start))" "$total" "$shards" \
	"$([ "$skipped" -gt 0 ] && printf ', %d mode-independent tests left to the default build' "$skipped")"
