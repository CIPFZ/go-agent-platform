#!/usr/bin/env bash
# annotate.sh TITLE FILE...: print the tails of FILEs as one GitHub Actions
# error annotation (no-op outside Actions).
[[ -n "${GITHUB_ACTIONS:-}" ]] || exit 0
title=$1
shift
for f in "$@"; do
	[[ -f "$f" ]] || continue
	echo "== $f"
	tail -40 "$f"
done | sed -E 's/%/%25/g' | awk -v t="$title" 'BEGIN{m=""} {m = m $0 "%0A"} END{print "::error title=" t "::" m}'
