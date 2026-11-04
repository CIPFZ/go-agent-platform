#!/bin/sh
# Rehearse install, upgrade and rollback in a temporary HOME without systemd
# and check that configuration and recordings survive (spec 13.2
# acceptance 4). Usage: deploy/linux/install_test.sh [BINARY]
set -eu
here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
export HOME=$work/home XDG_CONFIG_HOME= XDG_STATE_HOME= SYSTEMCTL=true
unset XDG_CONFIG_HOME XDG_STATE_HOME
mkdir -p "$HOME"
bin=${1:-}
if [ -z "$bin" ]; then
	bin=$work/vm-agent
	printf '#!/bin/sh\necho v1\n' >"$bin"
	chmod +x "$bin"
fi
fail() { echo "FAIL: $*" >&2; exit 1; }
lib=$HOME/.local/lib/vm-agent
env=$HOME/.config/vm-agent/vm-agent.env
state=$HOME/.local/state/vm-agent

"$here/install.sh" -b "$bin" -v 1.0.0 -l 127.0.0.1:7070 -d :5 >/dev/null
[ "$(readlink "$lib/current")" = "$lib/releases/1.0.0" ] || fail "current after install"
[ ! -e "$lib/previous" ] || fail "previous after first install"
grep -q '^VM_AGENT_ARGS=-listen 127.0.0.1:7070 ' "$env" || fail "env args"
grep -q '^DISPLAY=:5$' "$env" || fail "env display"
grep -q 'KillMode=process' "$HOME/.config/systemd/user/vm-agent.service" || fail "unit"
[ "$(stat -c %a "$env")" = 600 ] || fail "env mode"

echo '# operator edit' >>"$env"
mkdir -p "$state/records"
echo video >"$state/records/rec-1.mp4"
cp "$env" "$work/env.before"

"$here/install.sh" -b "$bin" -v 1.1.0 -l 0.0.0.0:9999 >/dev/null
[ "$(readlink "$lib/current")" = "$lib/releases/1.1.0" ] || fail "current after upgrade"
[ "$(readlink "$lib/previous")" = "$lib/releases/1.0.0" ] || fail "previous after upgrade"
cmp -s "$env" "$work/env.before" || fail "upgrade changed the configuration"
[ -f "$state/records/rec-1.mp4" ] || fail "upgrade lost a recording"

"$here/rollback.sh" >/dev/null
[ "$(readlink "$lib/current")" = "$lib/releases/1.0.0" ] || fail "current after rollback"
[ "$(readlink "$lib/previous")" = "$lib/releases/1.1.0" ] || fail "previous after rollback"
cmp -s "$env" "$work/env.before" || fail "rollback changed the configuration"
[ "$(cat "$state/records/rec-1.mp4")" = video ] || fail "rollback lost a recording"
"$here/rollback.sh" >/dev/null
[ "$(readlink "$lib/current")" = "$lib/releases/1.1.0" ] || fail "second rollback"

# Reinstalling the current version keeps previous; old releases are pruned.
for v in 1.2.0 1.3.0 1.4.0 1.5.0; do sleep 1; "$here/install.sh" -b "$bin" -v "$v" >/dev/null; done
[ "$(readlink "$lib/previous")" = "$lib/releases/1.4.0" ] || fail "previous after several upgrades"
n=$(ls "$lib/releases" | wc -l)
[ "$n" -le 3 ] || fail "$n releases kept"
"$here/install.sh" -b "$bin" -v 1.5.0 >/dev/null
[ "$(readlink "$lib/previous")" = "$lib/releases/1.4.0" ] || fail "same-version reinstall moved previous"

if "$here/install.sh" -b "$bin" -v 'bad version' >/dev/null 2>&1; then fail "bad version accepted"; fi
if "$here/install.sh" -b /nonexistent >/dev/null 2>&1; then fail "missing binary accepted"; fi
echo "PASS install, upgrade and rollback keep configuration and recordings"
