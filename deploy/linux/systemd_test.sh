#!/bin/sh
# End-to-end check of the systemd user service on a machine (or privileged
# container) with a running user manager and an X server on $DISPLAY. Run as
# the desktop user with two vm-agent builds whose versions differ:
#
#   DISPLAY=:0 deploy/linux/systemd_test.sh vm-agent-1.0.0 vm-agent-2.0.0
#
# It checks: the unit starts and serves; Restart=always brings the agent back
# after it is killed; process/launch programs survive restarts (KillMode=
# process) while exec commands do not; and an upgrade and rollback keep the
# configuration and a recording-directory file.
set -eu
v1=$1 v2=$2
here=$(cd "$(dirname "$0")" && pwd)
port=${PORT:-17777}
url=http://127.0.0.1:$port
fail() { echo "FAIL: $*" >&2; systemctl --user status vm-agent --no-pager >&2 || true; exit 1; }
ver() { curl -fsS --max-time 2 "$url/v1/probe" 2>/dev/null | jq -r .result.version; }
wait_version() {
	i=0
	while [ "$(ver || true)" != "$1" ]; do
		i=$((i + 1)); [ $i -lt 150 ] || fail "agent did not serve version $1"
		sleep 0.2
	done
}
alive() { kill -0 "$1" 2>/dev/null && ! grep -q '^State:.*Z' "/proc/$1/status"; }

"$here/install.sh" -b "$v1" -v 1.0.0 -l "127.0.0.1:$port" -d "${DISPLAY:-:0}" >/dev/null
wait_version 1.0.0
echo "ok   unit started, serving 1.0.0"
[ "$(curl -fsS "$url/v1/probe" | jq -r .result.display.ready)" = true ] || fail "display not ready"

launched=$(curl -fsS -X POST "$url/v1/process/launch" -d '{"argv":["sleep","600"]}' | jq -r .result.pid)
curl -sS --max-time 60 -X POST "$url/v1/exec" -H 'X-Timeout-Ms: 60000' -d '{"argv":["sh","-c","echo $$ > /tmp/vm-agent-exec.pid; exec sleep 600"]}' >/dev/null 2>&1 &
sleep 1
execpid=$(cat /tmp/vm-agent-exec.pid)

main=$(systemctl --user show -p MainPID --value vm-agent)
kill -9 "$main"
sleep 6 # RestartSec=5
wait_version 1.0.0
new=$(systemctl --user show -p MainPID --value vm-agent)
[ "$new" != "$main" ] || fail "agent not restarted"
echo "ok   Restart=always restarted the agent after SIGKILL ($main -> $new)"
sleep 1
alive "$launched" || fail "process/launch program $launched died with the agent"
! alive "$execpid" || fail "exec command $execpid survived the crash"
echo "ok   launched program survives a crash, exec command was reaped"

state=$HOME/.local/state/vm-agent
echo kept >"$state/records/rec-marker.mp4"
echo '# operator edit' >>"$HOME/.config/vm-agent/vm-agent.env"
cp "$HOME/.config/vm-agent/vm-agent.env" /tmp/vm-agent-env.before
"$here/install.sh" -b "$v2" -v 2.0.0 >/dev/null
wait_version 2.0.0
alive "$launched" || fail "launched program died during the upgrade"
echo "ok   upgraded to 2.0.0, launched program still running"
"$here/rollback.sh" >/dev/null
wait_version 1.0.0
cmp -s "$HOME/.config/vm-agent/vm-agent.env" /tmp/vm-agent-env.before || fail "configuration changed"
[ "$(cat "$state/records/rec-marker.mp4")" = kept ] || fail "recording lost"
alive "$launched" || fail "launched program died during the rollback"
echo "ok   rolled back to 1.0.0 with configuration and recordings intact"
kill "$launched"
echo PASS
