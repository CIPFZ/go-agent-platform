#!/usr/bin/env bash
# Multi-display matrix (spec 13.3): start an Xorg server with the dummy
# driver, whose outputs xrandr can arrange freely, and run
# TestMultiDisplayMatrix against it without a window manager so windows
# stay exactly where they are created. Needs xserver-xorg-core,
# xserver-xorg-video-dummy and x11-xserver-utils; Xorg is started through
# sudo when not running as root.
set -euo pipefail
cd "$(dirname "$0")/../.."
DISPLAY_NUM=${DISPLAY_NUM:-20}
conf=$(pwd)/test/integration/testdata/xorg-dummy.conf
log=$(mktemp)
sudo=""
[[ $(id -u) -eq 0 ]] || sudo="sudo"
$sudo Xorg ":$DISPLAY_NUM" -noreset -ac -nolisten tcp -config "$conf" -logfile "$log" >/dev/null 2>&1 &
xpid=$!
trap '$sudo kill $xpid 2>/dev/null || true; rm -f "$log"' EXIT
export DISPLAY=":$DISPLAY_NUM"
for _ in $(seq 1 100); do xrandr >/dev/null 2>&1 && break; sleep 0.1; done
if ! xrandr >/dev/null; then
	$sudo cat "$log" >"$log.copy" 2>/dev/null || true
	deploy/ci/annotate.sh "Xorg dummy did not start" "$log.copy"
	cat "$log.copy"
	exit 1
fi
VM_AGENT_INTEGRATION=1 VM_AGENT_MULTIDISPLAY=1 deploy/ci/gotest.sh -count=1 -v -run 'TestMultiDisplayMatrix' ./test/integration/
