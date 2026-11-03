#!/usr/bin/env bash
# Linux X11 CI stage (spec section 12): start Xvfb and openbox, run the
# integration suite, then the deploy/e2e.sh regression against a freshly
# built vm-agent. Used by .github/workflows/ci.yml and runnable in any
# Ubuntu 24.04 environment with: xvfb openbox mousepad dbus jq curl.
set -euo pipefail
cd "$(dirname "$0")/../.."

DISPLAY_NUM=${DISPLAY_NUM:-99}
SCREEN=${SCREEN:-1920x1080x24}
WORK=$(mktemp -d)
pids=()
cleanup() {
	for p in "${pids[@]}"; do kill "$p" 2>/dev/null || true; done
	rm -rf "$WORK"
}
trap cleanup EXIT

Xvfb ":$DISPLAY_NUM" -screen 0 "$SCREEN" -nolisten tcp >"$WORK/xvfb.log" 2>&1 &
pids+=($!)
export DISPLAY=":$DISPLAY_NUM"
for _ in $(seq 1 50); do [[ -S "/tmp/.X11-unix/X$DISPLAY_NUM" ]] && break; sleep 0.1; done
openbox >"$WORK/openbox.log" 2>&1 &
pids+=($!)
sleep 1

echo "== integration suite"
VM_AGENT_INTEGRATION=1 go test -count=1 -v ./test/integration/ 2>&1 | tee "$WORK/integration.log" | grep -E '^(--- |PASS|FAIL|ok)' || true
grep -q '^ok' "$WORK/integration.log" || { tail -50 "$WORK/integration.log"; exit 1; }

echo "== deploy/e2e.sh"
go build -o "$WORK/vm-agent" ./cmd/vm-agent
"$WORK/vm-agent" -listen 127.0.0.1:17070 -audit-log "$WORK/audit.jsonl" -job-dir "$WORK/jobs" -record-dir "$WORK/records" >"$WORK/agent.log" 2>&1 &
pids+=($!)
for _ in $(seq 1 50); do curl -fsS http://127.0.0.1:17070/v1/probe >/dev/null 2>&1 && break; sleep 0.1; done
AGENT_URL=http://127.0.0.1:17070 deploy/e2e.sh || { cat "$WORK/agent.log"; exit 1; }
