#!/usr/bin/env bash
# Run go test with the given arguments and, on failure, emit the failing
# tests with the output around them as GitHub Actions error annotations,
# which are readable on the public run page without access to the job log.
set -uo pipefail
log=$(mktemp)
go test "$@" 2>&1 | tee "$log"
status=${PIPESTATUS[0]}
if [[ $status -ne 0 && -n "${GITHUB_ACTIONS:-}" ]]; then
	# In -v output a test's messages precede its --- FAIL line, otherwise
	# they follow it; take context on both sides.
	grep -n -E -- '--- FAIL|^panic:|^FAIL\s|build failed|cannot|undefined' "$log" | head -8 | cut -d: -f1 | while read -r n; do
		start=$((n > 25 ? n - 25 : 1))
		sed -n "${start},$((n + 12))p" "$log" |
			sed -E 's/%/%25/g' | awk 'BEGIN{m=""} {m = m $0 "%0A"} END{print "::error title=go test failed::" m}'
	done
fi
rm -f "$log"
exit "$status"
