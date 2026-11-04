#!/bin/sh
# Install or upgrade vm-agent as a systemd user service of the desktop user
# (spec 4.1). Run as that user, not root:
#
#   deploy/linux/install.sh -b ./vm-agent-linux-amd64 [-v VERSION] [-l ADDR]
#                            [-d DISPLAY] [-x XAUTHORITY] [--no-start]
#
# Layout (all under the user's home):
#   ~/.local/lib/vm-agent/releases/<version>/vm-agent   one directory per release
#   ~/.local/lib/vm-agent/current, previous              symlinks to releases
#   ~/.config/vm-agent/vm-agent.env                       arguments and display;
#                                                         created once, never overwritten
#   ~/.local/state/vm-agent/{audit,jobs,records}          audit log, jobs, recordings
#   ~/.config/systemd/user/vm-agent.service               the unit
#
# An upgrade installs the new release, keeps the old one as "previous" and
# restarts the service; deploy/linux/rollback.sh switches back. Neither touches
# the configuration or the state directory. Set SYSTEMCTL=true to rehearse
# without systemd.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
PREFIX=${VM_AGENT_PREFIX:-$HOME/.local/lib/vm-agent}
CONFIG_HOME=${XDG_CONFIG_HOME:-$HOME/.config}
STATE_DIR=${VM_AGENT_STATE_DIR:-${XDG_STATE_HOME:-$HOME/.local/state}/vm-agent}
CONFIG_DIR=$CONFIG_HOME/vm-agent
UNIT_DIR=$CONFIG_HOME/systemd/user
SYSTEMCTL=${SYSTEMCTL:-systemctl}
KEEP_RELEASES=${KEEP_RELEASES:-3}

binary= version= listen=0.0.0.0:7070 display=${DISPLAY:-:0} xauthority=${XAUTHORITY:-$HOME/.Xauthority} start=1
usage() { echo "usage: $0 -b BINARY [-v VERSION] [-l LISTEN] [-d DISPLAY] [-x XAUTHORITY] [--no-start]" >&2; exit 2; }
while [ $# -gt 0 ]; do
	case $1 in
	-b) binary=$2; shift 2 ;;
	-v) version=$2; shift 2 ;;
	-l) listen=$2; shift 2 ;;
	-d) display=$2; shift 2 ;;
	-x) xauthority=$2; shift 2 ;;
	--no-start) start=0; shift ;;
	*) usage ;;
	esac
done
[ -n "$binary" ] || usage
[ -x "$binary" ] || { echo "install: $binary is not an executable file" >&2; exit 2; }
[ "$(id -u)" != 0 ] || { echo "install: run as the desktop user, not root (spec 4.1)" >&2; exit 2; }
[ -n "$version" ] || version=$(date -u +%Y%m%d%H%M%S)
case $version in *[!A-Za-z0-9._+-]*) echo "install: invalid version '$version'" >&2; exit 2 ;; esac

release=$PREFIX/releases/$version
mkdir -p "$release" "$CONFIG_DIR" "$UNIT_DIR" "$STATE_DIR/audit" "$STATE_DIR/jobs" "$STATE_DIR/records"
chmod 700 "$STATE_DIR"
install -m 0755 "$binary" "$release/vm-agent.tmp"
mv -f "$release/vm-agent.tmp" "$release/vm-agent"

# Atomic symlink replacement: create beside, then rename over.
relink() { ln -sfn "$2" "$1.tmp" && mv -fT "$1.tmp" "$1"; }
if [ -L "$PREFIX/current" ]; then
	old=$(readlink "$PREFIX/current")
	if [ "$old" != "$release" ]; then
		relink "$PREFIX/previous" "$old"
	fi
fi
relink "$PREFIX/current" "$release"

env_file=$CONFIG_DIR/vm-agent.env
if [ ! -e "$env_file" ]; then
	umask 077
	cat >"$env_file" <<CONF
# vm-agent configuration, kept across upgrades and rollbacks. Arguments are
# those of spec 4.3; DISPLAY and XAUTHORITY locate the X session.
VM_AGENT_ARGS=-listen $listen -audit-log $STATE_DIR/audit/audit.jsonl -job-dir $STATE_DIR/jobs -record-dir $STATE_DIR/records
DISPLAY=$display
XAUTHORITY=$xauthority
CONF
	echo "install: wrote $env_file"
else
	echo "install: keeping existing $env_file"
fi
install -m 0644 "$here/vm-agent.service" "$UNIT_DIR/vm-agent.service"

# Keep the newest releases plus whatever current and previous point to.
cur=$(readlink "$PREFIX/current" || true)
prev=$(readlink "$PREFIX/previous" 2>/dev/null || true)
ls -1t "$PREFIX/releases" | tail -n +$((KEEP_RELEASES + 1)) | while read -r r; do
	d=$PREFIX/releases/$r
	[ "$d" = "$cur" ] || [ "$d" = "$prev" ] || rm -rf "$d"
done

$SYSTEMCTL --user daemon-reload
$SYSTEMCTL --user enable vm-agent.service >/dev/null
if [ "$start" = 1 ]; then
	$SYSTEMCTL --user restart vm-agent.service
fi
echo "install: vm-agent $version installed (current -> $release)"
