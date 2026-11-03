#!/usr/bin/env bash
# Linux regression script (spec section 12): drive a real text editor through
# a running vm-agent — launch it on a file, activate its window, type, save,
# capture the screen, read the file back and close the editor.
#
#   AGENT_URL=http://127.0.0.1:7070 EDITOR_CMD=mousepad deploy/e2e.sh
#
# Requires curl and jq on the machine running the script; the editor must be
# installed in the VM. Exits non-zero on the first failed step.
set -euo pipefail

AGENT_URL=${AGENT_URL:-http://127.0.0.1:7070}
EDITOR_CMD=${EDITOR_CMD:-mousepad}
CURL_TIMEOUT=${CURL_TIMEOUT:-30}
command -v curl >/dev/null || { echo "curl is required" >&2; exit 2; }
command -v jq >/dev/null || { echo "jq is required" >&2; exit 2; }

step() { printf '==> %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

rid() { printf 'e2e-%s-%04d' "$(date +%s%N)" "$RANDOM"; }

# call METHOD PATH [JSON] -> prints the result object, fails on ok=false.
call() {
	local method=$1 path=$2 body=${3:-} out
	if [[ -n "$body" ]]; then
		out=$(curl -sS --max-time "$CURL_TIMEOUT" -X "$method" -H 'Content-Type: application/json' \
			-H "X-Request-Id: $(rid)" --data "$body" "$AGENT_URL$path")
	else
		out=$(curl -sS --max-time "$CURL_TIMEOUT" -X "$method" -H "X-Request-Id: $(rid)" "$AGENT_URL$path")
	fi
	if [[ $(jq -r '.ok' <<<"$out") != true ]]; then
		fail "$method $path: $(jq -c '.error' <<<"$out")"
	fi
	jq -c '.result' <<<"$out"
}

step "probe"
probe=$(call GET /v1/probe)
[[ $(jq -r '.display.ready' <<<"$probe") == true ]] || fail "display not ready: $probe"

step "prepare the document"
home=$(call GET /v1/file/known_dirs | jq -r '.home')
name="vm-agent-e2e-$(date +%s).txt"
doc="$home/$name"
call POST /v1/file/write "$(jq -cn --arg p "$doc" '{path:$p, content:"", overwrite:true}')" >/dev/null

step "launch $EDITOR_CMD"
pid=$(call POST /v1/process/launch "$(jq -cn --arg e "$EDITOR_CMD" --arg p "$doc" '{argv:[$e,$p]}')" | jq -r '.pid')
cleanup() { (call POST /v1/exec "$(jq -cn --arg p "$pid" '{argv:["kill",$p]}')") >/dev/null 2>&1 || true; }
trap cleanup EXIT

step "wait for the editor window"
win=
for _ in $(seq 1 60); do
	win=$(call GET '/v1/window/list' | jq -r --arg n "$name" '[.windows[] | select(.title | contains($n))][0].id // empty')
	[[ -n "$win" ]] && break
	sleep 0.5
done
[[ -n "$win" ]] || fail "no window titled $name"

step "activate window $win"
active=$(call POST /v1/window/activate "$(jq -cn --arg id "$win" '{id:$id}')")
[[ $(jq -r '.active' <<<"$active") == true ]] || fail "window not active: $active"
cx=$(jq -r '.client_rect.x + (.client_rect.w / 2 | floor)' <<<"$active")
cy=$(jq -r '.client_rect.y + (.client_rect.h / 2 | floor)' <<<"$active")
call POST /v1/input/mouse/click "$(jq -cn --argjson x "$cx" --argjson y "$cy" '{x:$x, y:$y}')" >/dev/null

step "type and save"
text="vm-agent e2e $(date -u +%Y-%m-%dT%H:%M:%SZ) ünïcødé 世界"
call POST /v1/input/key/type "$(jq -cn --arg t "$text" '{text:$t}')" >/dev/null
call POST /v1/input/key/hotkey '{"keys":["ctrl","s"]}' >/dev/null

step "read the saved file"
content=
for _ in $(seq 1 40); do
	content=$(call POST /v1/file/read "$(jq -cn --arg p "$doc" '{path:$p}')" | jq -r '.content' | base64 -d)
	[[ "$content" == *"$text"* ]] && break
	sleep 0.25
done
[[ "$content" == *"$text"* ]] || fail "file content is '$content', want '$text'"

step "capture the screen"
shot=$(call POST /v1/screen/capture '{"cursor":true}')
[[ $(jq -r '.width' <<<"$shot") -gt 0 && $(jq -r '.image | length' <<<"$shot") -gt 1000 ]] || fail "empty capture"
printf '    %sx%s jpeg, sha256 %s\n' "$(jq -r .width <<<"$shot")" "$(jq -r .height <<<"$shot")" "$(jq -r .sha256 <<<"$shot")"

step "close the editor"
call POST /v1/window/close "$(jq -cn --arg id "$win" '{id:$id}')" >/dev/null
for _ in $(seq 1 40); do
	left=$(call GET '/v1/window/list' | jq -r --arg id "$win" '[.windows[] | select(.id == $id)] | length')
	[[ "$left" == 0 ]] && break
	sleep 0.25
done
[[ "$left" == 0 ]] || fail "editor window still open"
(call POST /v1/exec "$(jq -cn --arg p "$doc" '{argv:["rm","-f",$p]}')") >/dev/null 2>&1 || true
trap - EXIT
echo "PASS"
