#!/bin/sh
set -eu

# Install a release and systemd units. ROOT enables an unprivileged staging
# rehearsal; leave it empty for a real host installation.
ROOT=${ROOT:-}
PREFIX=${PREFIX:-/usr/local}
VERSION=${VERSION:-$(date -u +%Y%m%d%H%M%S)}
ENABLE=${ENABLE:-0}

usage() {
	printf '%s\n' "usage: $0 GATEWAY_BINARY AGENT_BINARY" >&2
	exit 2
}
[ "$#" = 2 ] || usage
GATEWAY=$1
AGENT=$2
[ -x "$GATEWAY" ] && [ -x "$AGENT" ] || { printf '%s\n' 'release binaries must be executable' >&2; exit 2; }

root_path() {
	case "$1" in
		/*) printf '%s%s\n' "$ROOT" "$1" ;;
		*) printf '%s/%s\n' "$ROOT" "$1" ;;
	esac
}

target_prefix=$(root_path "$PREFIX")
state_dir=$(root_path /var/lib/go-agent-gateway)
agent_state_dir=$(root_path /var/lib/go-guest-agent)
unit_dir=$(root_path /etc/systemd/system)
mkdir -p "$state_dir/artifacts" "$agent_state_dir/workspace" "$unit_dir"
PREFIX="$target_prefix" deploy/release.sh install "$VERSION" "$GATEWAY" "$AGENT"
install -m 0644 deploy/systemd/go-agent-gateway.service "$unit_dir/go-agent-gateway.service"
install -m 0644 deploy/systemd/go-guest-agent.service "$unit_dir/go-guest-agent.service"

if [ -z "$ROOT" ] && [ "$(id -u)" = 0 ]; then
	if ! id -u agent-gateway >/dev/null 2>&1; then useradd --system --home-dir /var/lib/go-agent-gateway --shell /usr/sbin/nologin agent-gateway; fi
	if ! id -u guest-agent >/dev/null 2>&1; then useradd --system --home-dir /var/lib/go-guest-agent --shell /usr/sbin/nologin guest-agent; fi
	chown -R agent-gateway:agent-gateway "$state_dir"
	chown -R guest-agent:guest-agent "$agent_state_dir"
	if [ "$ENABLE" = 1 ]; then
		systemctl daemon-reload
		systemctl enable --now go-agent-gateway.service go-guest-agent.service
	fi
elif [ "$ENABLE" = 1 ]; then
	printf '%s\n' 'ENABLE=1 requires a real systemd host (omit ROOT)' >&2
	exit 2
fi
printf 'installed %s under %s\n' "$VERSION" "${ROOT:-/}"
