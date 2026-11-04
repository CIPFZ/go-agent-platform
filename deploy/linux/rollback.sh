#!/bin/sh
# Switch vm-agent back to the previous release and restart it. The
# configuration (~/.config/vm-agent) and state (~/.local/state/vm-agent:
# audit log, jobs, recordings) are left untouched. Running it twice returns
# to the newer release.
set -eu
PREFIX=${VM_AGENT_PREFIX:-$HOME/.local/lib/vm-agent}
SYSTEMCTL=${SYSTEMCTL:-systemctl}
[ -L "$PREFIX/previous" ] || { echo "rollback: no previous release in $PREFIX" >&2; exit 1; }
cur=$(readlink "$PREFIX/current")
prev=$(readlink "$PREFIX/previous")
[ -x "$prev/vm-agent" ] || { echo "rollback: $prev/vm-agent is missing" >&2; exit 1; }
relink() { ln -sfn "$2" "$1.tmp" && mv -fT "$1.tmp" "$1"; }
relink "$PREFIX/current" "$prev"
relink "$PREFIX/previous" "$cur"
$SYSTEMCTL --user restart vm-agent.service
echo "rollback: current -> $prev (previous -> $cur)"
