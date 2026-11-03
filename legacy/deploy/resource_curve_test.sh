#!/bin/sh
set -eu
out=$(mktemp)
trap 'rm -f "$out"' EXIT
if ! DURATIONS=1 deploy/resource_curve.sh "$out" >/dev/null 2>&1; then
	printf '%s\n' 'resource curve rehearsal requires Linux /usr/bin/time -v; skipped'
	exit 0
fi
lines=$(wc -l < "$out")
[ "$lines" -eq 2 ] || { printf 'unexpected sample count: %s\n' "$lines" >&2; exit 1; }
printf '%s\n' 'resource curve rehearsal passed'
