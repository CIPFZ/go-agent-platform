#!/usr/bin/env bash
set -euo pipefail

GATEWAY_URL=${GATEWAY_URL:-http://127.0.0.1:18090}
GATEWAY_TOKEN_FILE=${GATEWAY_TOKEN_FILE:-/tmp/go-agent-e2e-token}
AGENT_ID=${AGENT_ID:?AGENT_ID is required}
CURL_TIMEOUT=${CURL_TIMEOUT:-15}
POLL_INTERVAL=${POLL_INTERVAL:-1}
POLL_TIMEOUT=${POLL_TIMEOUT:-45}

command -v curl >/dev/null
command -v jq >/dev/null

TOKEN=$(<"$GATEWAY_TOKEN_FILE")
AUTH=(-H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json')
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

request_id() {
  printf 'vm-functional-e2e-%s-%s' "$(date +%s%N)" "$RANDOM"
}

envelope() {
  local payload=$1
  jq -cn --arg rid "$(request_id)" --argjson payload "$payload" \
    '$payload + {protocol_version:"1", request_id:$rid, timestamp:(now|todateiso8601)}'
}

create_task() {
  local payload=$1
  local out="$WORK/create-$RANDOM.json"
  local code
  code=$(curl -sS --max-time "$CURL_TIMEOUT" "${AUTH[@]}" -o "$out" -w '%{http_code}' \
    -X POST "$GATEWAY_URL/api/v1/tasks" --data "$(envelope "$payload")")
  if [[ "$code" != 2* ]]; then
    cat "$out" >&2
    return 1
  fi
  jq -r '.task_id' "$out"
}

wait_task() {
  local task_id=$1
  local deadline=$((SECONDS + POLL_TIMEOUT))
  while (( SECONDS < deadline )); do
    curl -fsS --max-time "$CURL_TIMEOUT" "${AUTH[@]}" \
      "$GATEWAY_URL/api/v1/tasks/$task_id" >"$WORK/task.json"
    local status
    status=$(jq -r '.status' "$WORK/task.json")
    case "$status" in
      succeeded|failed|cancelled) return 0 ;;
    esac
    sleep "$POLL_INTERVAL"
  done
  echo "timeout waiting for task $task_id" >&2
  return 1
}

assert_value() {
  local filter=$1
  local expected=$2
  local actual
  actual=$(jq -r "$filter" "$WORK/task.json")
  [[ "$actual" == "$expected" ]] || { echo "assertion failed: $filter = $actual (expected $expected)" >&2; return 1; }
}

run_task() {
  local payload=$1
  local id
  id=$(create_task "$payload")
  wait_task "$id"
  printf '%s\n' "$id"
}

agent_payload() {
  local action=$1
  local key=$2
  jq -cn --arg agent "$AGENT_ID" --arg key "$key" --argjson action "$action" \
    '{agent_id:$agent, idempotency_key:$key, action:$action}'
}

printf 'case command.exec\n'
action=$(jq -cn '{type:"command.exec",command:{argv:["printf","vm-command-ok"]}}')
id=$(run_task "$(agent_payload "$action" "vm-command-1")")
assert_value '.status' succeeded
assert_value '.result.stdout' vm-command-ok

printf 'case idempotency replay and conflict\n'
action=$(jq -cn '{type:"command.exec",command:{argv:["printf","idempotent"]}}')
payload=$(agent_payload "$action" "vm-idempotency-1")
first=$(create_task "$payload")
second=$(create_task "$payload")
[[ "$first" == "$second" ]] || { echo "idempotency replay returned different task" >&2; exit 1; }
wait_task "$first"
assert_value '.result.stdout' idempotent
conflict_action=$(jq -cn '{type:"command.exec",command:{argv:["printf","conflict"]}}')
conflict=$(curl -sS --max-time "$CURL_TIMEOUT" "${AUTH[@]}" -o "$WORK/conflict.json" -w '%{http_code}' \
  -X POST "$GATEWAY_URL/api/v1/tasks" --data "$(envelope "$(agent_payload "$conflict_action" "vm-idempotency-1")")")
[[ "$conflict" == 409 ]] || { echo "expected idempotency conflict 409, got $conflict" >&2; exit 1; }

printf 'case file write/read/artifact\n'
write_action=$(jq -cn --arg b64 'dm0tZmlsZS1jb250ZW50' '{type:"file.write",file:{path:"functional-e2e.txt",content:$b64,overwrite:true}}')
id=$(run_task "$(agent_payload "$write_action" "vm-file-write-1")")
assert_value '.status' succeeded
artifact=$(jq -r '.result.artifact_id' "$WORK/task.json")
[[ -n "$artifact" && "$artifact" != null ]] || { echo 'missing artifact id' >&2; exit 1; }
curl -fsS --max-time "$CURL_TIMEOUT" "${AUTH[@]}" "$GATEWAY_URL/api/v1/artifacts/$artifact" -o "$WORK/artifact.bin"
[[ "$(cat "$WORK/artifact.bin")" == vm-file-content ]] || { echo 'artifact content mismatch' >&2; exit 1; }
read_action=$(jq -cn '{type:"file.read",file:{path:"functional-e2e.txt",max_bytes:1024}}')
id=$(run_task "$(agent_payload "$read_action" "vm-file-read-1")")
assert_value '.status' succeeded
assert_value '.result.content' vm-file-content

printf 'case file overwrite denial\n'
write_no_overwrite=$(jq -cn --arg b64 'bmV3' '{type:"file.write",file:{path:"functional-e2e.txt",content:$b64,overwrite:false}}')
id=$(run_task "$(agent_payload "$write_no_overwrite" "vm-file-overwrite-deny")")
assert_value '.status' failed
assert_value '.error.code' file_exists

printf 'case symlink boundary\n'
link_action=$(jq -cn '{type:"command.exec",command:{argv:["ln","-s","/etc/passwd","outside-link"]}}')
id=$(run_task "$(agent_payload "$link_action" "vm-symlink-create")")
assert_value '.status' succeeded
read_link=$(jq -cn '{type:"file.read",file:{path:"outside-link",max_bytes:1024}}')
id=$(run_task "$(agent_payload "$read_link" "vm-symlink-read")")
assert_value '.status' failed
assert_value '.error.code' invalid_path

printf 'case file size boundary\n'
large_action=$(jq -cn '{type:"command.exec",command:{argv:["dd","if=/dev/zero","of=large-e2e.bin","bs=1M","count=3"]}}')
id=$(run_task "$(agent_payload "$large_action" "vm-large-file-create")")
assert_value '.status' succeeded
large_read=$(jq -cn '{type:"file.read",file:{path:"large-e2e.bin",max_bytes:64}}')
id=$(run_task "$(agent_payload "$large_read" "vm-large-file-read")")
assert_value '.status' failed
assert_value '.error.code' file_too_large

printf 'case output limit\n'
output_action=$(jq -cn '{type:"command.exec",command:{argv:["printf","1234567890"],output_limit_bytes:4}}')
id=$(run_task "$(agent_payload "$output_action" "vm-output-limit")")
assert_value '.status' succeeded
assert_value '.result.stdout' 1234
assert_value '.result.error_code' output_limited

printf 'case command timeout\n'
timeout_action=$(jq -cn '{type:"command.exec",command:{argv:["sleep","5"],timeout_seconds:1}}')
id=$(run_task "$(agent_payload "$timeout_action" "vm-timeout")")
assert_value '.status' failed
assert_value '.error.code' timeout

printf 'case process tree cancellation\n'
cancel_action=$(jq -cn '{type:"command.exec",command:{argv:["sh","-c","sleep 30 & child=$!; printf %s $child > child-e2e.pid; wait"]}}')
cancel_id=$(create_task "$(agent_payload "$cancel_action" "vm-cancel-tree")")
sleep 1
cancel_code=$(curl -sS --max-time "$CURL_TIMEOUT" "${AUTH[@]}" -o "$WORK/cancel.json" -w '%{http_code}' \
  -X POST "$GATEWAY_URL/api/v1/tasks/$cancel_id/cancel" --data "$(envelope '{}')")
[[ "$cancel_code" == 2* ]] || { cat "$WORK/cancel.json" >&2; exit 1; }
wait_task "$cancel_id"
assert_value '.status' cancelled
child_check=$(jq -cn '{type:"command.exec",command:{argv:["sh","-c","if kill -0 $(cat child-e2e.pid) 2>/dev/null; then exit 1; fi"]}}')
id=$(run_task "$(agent_payload "$child_check" "vm-cancel-tree-check")")
assert_value '.status' succeeded

printf 'case policy denial\n'
policy_action=$(jq -cn '{type:"command.exec",command:{argv:["rm","-f","blocked"]}}')
id=$(run_task "$(agent_payload "$policy_action" "vm-policy-deny")")
assert_value '.status' failed
assert_value '.error.code' policy_denied

printf 'case screenshot on headless VM\n'
screenshot_action=$(jq -cn '{type:"desktop.screenshot",screenshot:{output_path:"screen.png"}}')
id=$(run_task "$(agent_payload "$screenshot_action" "vm-screenshot-headless")")
assert_value '.status' failed
assert_value '.error.code' no_display

printf 'VM functional E2E passed for agent %s\n' "$AGENT_ID"
