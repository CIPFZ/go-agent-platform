#!/bin/sh
set -eu

# Collect repeatable HTTP benchmark and process-resource samples on Linux.
# Override DURATIONS with a comma-separated list of benchmark seconds.
OUT=${1:-resource-curve.csv}
DURATIONS=${DURATIONS:-5,30,60}
TIME_BIN=${TIME_BIN:-/usr/bin/time}

case "$OUT" in
	/*) ;;
	*) OUT="$PWD/$OUT" ;;
esac
mkdir -p "$(dirname "$OUT")"
printf '%s\n' 'duration_seconds,elapsed_seconds,max_rss_kb' > "$OUT"
old_ifs=$IFS
IFS=,
set -- $DURATIONS
IFS=$old_ifs
for duration in "$@"; do
	case "$duration" in *[!0-9]*|'0') printf '%s\n' 'DURATIONS must contain positive integers' >&2; exit 2;; esac
	metrics=$(mktemp)
	trap 'rm -f "$metrics"' EXIT HUP INT TERM
	"$TIME_BIN" -v -o "$metrics" go test ./internal/gateway -run '^$' -bench '^BenchmarkHTTP' -benchtime="${duration}s" -count=1 >/dev/null
	elapsed=$(awk -F': ' '/Elapsed \(wall clock\) time/ {print $2}' "$metrics")
	rss=$(awk -F': ' '/Maximum resident set size/ {print $2}' "$metrics")
	[ -n "$elapsed" ] && [ -n "$rss" ] || { printf '%s\n' 'time output did not contain elapsed time and RSS' >&2; exit 1; }
	printf '%s,%s,%s\n' "$duration" "$elapsed" "$rss" >> "$OUT"
	rm -f "$metrics"
	trap - EXIT HUP INT TERM
done
cat "$OUT"
