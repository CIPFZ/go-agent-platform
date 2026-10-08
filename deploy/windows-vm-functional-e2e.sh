#!/usr/bin/env bash
set -euo pipefail

GATEWAY_URL=${GATEWAY_URL:-http://127.0.0.1:18090}
GATEWAY_TOKEN_FILE=${GATEWAY_TOKEN_FILE:-/tmp/go-agent-win-token}
AGENT_ID=${AGENT_ID:?AGENT_ID is required}
CURL_TIMEOUT=${CURL_TIMEOUT:-15}
POLL_TIMEOUT=${POLL_TIMEOUT:-60}
command -v curl >/dev/null
command -v jq >/dev/null
TOKEN=$(<"$GATEWAY_TOKEN_FILE")
AUTH=(-H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json')
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

request_id() { printf 'windows-vm-e2e-%s-%s' "$(date +%s%N)" "$RANDOM"; }
envelope() {
  local payload=$1
  jq -cn --arg rid "$(request_id)" --argjson payload "$payload" \
    '$payload + {protocol_version:"1", request_id:$rid, timestamp:(now|todateiso8601)}'
}
create_task() {
  local payload=$1 out="$WORK/create-$RANDOM.json" code
  code=$(curl -sS --max-time "$CURL_TIMEOUT" "${AUTH[@]}" -o "$out" -w '%{http_code}' \
    -X POST "$GATEWAY_URL/api/v1/tasks" --data "$(envelope "$payload")")
  if [[ "$code" != 2* ]]; then cat "$out" >&2; return 1; fi
  jq -r '.task.task_id' "$out"
}
wait_task() {
  local id=$1 deadline=$((SECONDS + POLL_TIMEOUT)) status
  while (( SECONDS < deadline )); do
    curl -fsS --max-time "$CURL_TIMEOUT" "${AUTH[@]}" "$GATEWAY_URL/api/v1/tasks/$id" >"$WORK/task.json"
    status=$(jq -r '.task.status' "$WORK/task.json")
    case "$status" in succeeded|failed|cancelled) return 0;; esac
    sleep 1
  done
  echo "timeout waiting for task $id" >&2
  return 1
}
assert_value() {
  local filter=$1 expected=$2 actual
  actual=$(jq -r ".task$filter" "$WORK/task.json")
  [[ "$actual" == "$expected" ]] || { echo "assertion failed: $filter = $actual (expected $expected)" >&2; return 1; }
}
payload() {
  local action=$1 key=$2
  jq -cn --arg agent "$AGENT_ID" --arg key "$key" --argjson action "$action" \
    '{caller_id:"windows-vm-e2e",agent_id:$agent,idempotency_key:$key,action:$action}'
}
run_task() { local id; id=$(create_task "$1"); wait_task "$id"; printf '%s\n' "$id"; }
PS_JSON='["powershell.exe","-NoProfile","-NonInteractive","-Command"]'

printf 'case Windows command.exec\n'
action=$(jq -cn --argjson ps "$PS_JSON" '{type:"command.exec",command:{argv:($ps+["[Console]::Out.Write(\u0027win-command-ok\u0027)"])}}')
id=$(run_task "$(payload "$action" win-command-1)")
assert_value '.status' succeeded
assert_value '.result.stdout' win-command-ok

printf 'case idempotency replay and conflict\n'
action=$(jq -cn --argjson ps "$PS_JSON" '{type:"command.exec",command:{argv:($ps+["[Console]::Out.Write(\u0027idempotent\u0027)"])}}')
body=$(payload "$action" win-idempotency-1)
first=$(create_task "$body")
second=$(create_task "$body")
[[ "$first" == "$second" ]] || { echo 'idempotency mismatch' >&2; exit 1; }
wait_task "$first"
assert_value '.result.stdout' idempotent
conflict_action=$(jq -cn --argjson ps "$PS_JSON" '{type:"command.exec",command:{argv:($ps+["[Console]::Out.Write(\u0027conflict\u0027)"])}}')
code=$(curl -sS --max-time "$CURL_TIMEOUT" "${AUTH[@]}" -o "$WORK/conflict.json" -w '%{http_code}' \
  -X POST "$GATEWAY_URL/api/v1/tasks" --data "$(envelope "$(payload "$conflict_action" win-idempotency-1)")")
[[ "$code" == 409 ]] || { echo "expected 409, got $code" >&2; exit 1; }

printf 'case Windows file write/read/artifact\n'
write_action=$(jq -cn --arg b64 d2luLWZpbGUtY29udGVudA== '{type:"file.write",file:{path:"functional-e2e.txt",content:$b64,overwrite:true}}')
id=$(run_task "$(payload "$write_action" win-file-write-1)")
assert_value '.status' succeeded
artifact=$(jq -r '.task.result.artifacts[0].artifact_id' "$WORK/task.json")
curl -fsS --max-time "$CURL_TIMEOUT" "${AUTH[@]}" "$GATEWAY_URL/api/v1/tasks/$id/artifacts/$artifact" -o "$WORK/artifact.bin"
[[ "$(cat "$WORK/artifact.bin")" == win-file-content ]] || { echo 'artifact mismatch' >&2; exit 1; }
read_action=$(jq -cn '{type:"file.read",file:{path:"functional-e2e.txt",max_bytes:1024}}')
id=$(run_task "$(payload "$read_action" win-file-read-1)")
assert_value '.status' succeeded
assert_value '.result.stdout' win-file-content

printf 'case overwrite and path boundary\n'
no_overwrite=$(jq -cn --arg b64 bmV3 '{type:"file.write",file:{path:"functional-e2e.txt",content:$b64,overwrite:false}}')
id=$(run_task "$(payload "$no_overwrite" win-overwrite-deny)")
assert_value '.status' failed
assert_value '.result.error_code' file_exists
outside=$(jq -cn '{type:"file.read",file:{path:"..\\Windows\\System32\\drivers\\etc\\hosts",max_bytes:1024}}')
id=$(run_task "$(payload "$outside" win-path-boundary)")
assert_value '.status' failed
assert_value '.result.error_code' invalid_path

printf 'case file size boundary\n'
large=$(jq -cn --argjson ps "$PS_JSON" '{type:"command.exec",command:{argv:($ps+["$f=[IO.File]::Create(\u0027large-e2e.bin\u0027);$f.SetLength(3145728);$f.Close()"] )}}')
id=$(run_task "$(payload "$large" win-large-create)")
assert_value '.status' succeeded
large_read=$(jq -cn '{type:"file.read",file:{path:"large-e2e.bin",max_bytes:64}}')
id=$(run_task "$(payload "$large_read" win-large-read)")
assert_value '.status' failed
assert_value '.result.error_code' file_too_large

printf 'case output limit and timeout\n'
output=$(jq -cn --argjson ps "$PS_JSON" '{type:"command.exec",command:{argv:($ps+["[Console]::Out.Write(\u00271234567890\u0027)"]),output_limit_bytes:4}}')
id=$(run_task "$(payload "$output" win-output-limit)")
assert_value '.status' succeeded
assert_value '.result.stdout' 1234
assert_value '.result.error_code' output_limited
timeout_action=$(jq -cn --argjson ps "$PS_JSON" '{type:"command.exec",command:{argv:($ps+["Start-Sleep -Seconds 5"]),timeout_seconds:1}}')
id=$(run_task "$(payload "$timeout_action" win-timeout)")
assert_value '.status' failed
assert_value '.result.error_code' timeout

printf 'case process tree cancellation\n'
tree=$(jq -cn --argjson ps "$PS_JSON" '{type:"command.exec",command:{argv:($ps+["$p=Start-Process ping.exe -ArgumentList \u0027-t\u0027,\u0027127.0.0.1\u0027 -PassThru;$p.Id | Set-Content child-e2e.pid;$p.WaitForExit()"] )}}')
cancel_id=$(create_task "$(payload "$tree" win-cancel-tree)")
sleep 2
curl -fsS --max-time "$CURL_TIMEOUT" "${AUTH[@]}" -X POST "$GATEWAY_URL/api/v1/tasks/$cancel_id/cancel" --data "$(envelope '{}')" >/dev/null
wait_task "$cancel_id"
assert_value '.status' cancelled
check=$(jq -cn --argjson ps "$PS_JSON" '{type:"command.exec",command:{argv:($ps+["if(Get-Process -Id (Get-Content child-e2e.pid) -ErrorAction SilentlyContinue){exit 1}"])}}')
id=$(run_task "$(payload "$check" win-cancel-tree-check)")
assert_value '.status' succeeded

printf 'case policy denial and screenshot capability\n'
policy=$(jq -cn '{type:"command.exec",command:{argv:["rm","blocked"]}}')
id=$(run_task "$(payload "$policy" win-policy-deny)")
assert_value '.status' failed
assert_value '.result.error_code' policy_denied
screenshot=$(jq -cn '{type:"desktop.screenshot",screenshot:{output_path:"screen.png"}}')
id=$(run_task "$(payload "$screenshot" win-screenshot)")
assert_value '.status' failed
assert_value '.result.error_code' no_display

printf 'Windows VM functional E2E passed for agent %s\n' "$AGENT_ID"
