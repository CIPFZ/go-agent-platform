#!/usr/bin/env bash
# Run go test with the given arguments and, on failure, emit the failing
# tests and their first messages as GitHub Actions error annotations, which
# are readable on the public run page without access to the job log.
set -uo pipefail
log=$(mktemp)
go test "$@" 2>&1 | tee "$log"
status=${PIPESTATUS[0]}
if [[ $status -ne 0 && -n "${GITHUB_ACTIONS:-}" ]]; then
	grep -E -A4 -- '^\s*--- FAIL|^panic:|^FAIL\s|cannot|undefined' "$log" | head -60 |
		sed -E 's/%/%25/g' | awk 'BEGIN{msg=""} {msg = msg $0 "%0A"} END{print "::error title=go test failed::" msg}'
fi
rm -f "$log"
exit "$status"
