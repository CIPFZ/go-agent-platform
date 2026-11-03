#!/bin/sh
set -eu

# Local release manager. The default PREFIX matches the systemd unit paths;
# PREFIX may be overridden for an unprivileged rehearsal.
PREFIX=${PREFIX:-/usr/local}
VERSION=
COMMAND=

usage() {
	printf '%s\n' "usage: $0 install VERSION GATEWAY_BINARY AGENT_BINARY" "       $0 rollback" "       $0 current" >&2
	exit 2
}

[ "$#" -ge 1 ] || usage
COMMAND=$1
shift
case "$COMMAND" in
install)
	[ "$#" = 3 ] || usage
	VERSION=$1
	GATEWAY=$2
	AGENT=$3
	case "$VERSION" in *[!A-Za-z0-9._-]*|'') printf '%s\n' 'invalid release version' >&2; exit 2;; esac
	[ -x "$GATEWAY" ] && [ -x "$AGENT" ] || { printf '%s\n' 'release binaries must be executable' >&2; exit 2; }
	release_dir="$PREFIX/lib/go-agent-platform/releases/$VERSION"
	bin_dir="$PREFIX/bin"
	current="$PREFIX/lib/go-agent-platform/current"
	previous="$PREFIX/lib/go-agent-platform/previous"
	mkdir -p "$release_dir/bin" "$bin_dir"
	install -m 0755 "$GATEWAY" "$release_dir/bin/go-agent-gateway"
	install -m 0755 "$AGENT" "$release_dir/bin/go-guest-agent"
	if [ -L "$current" ]; then
		old=$(readlink "$current")
		ln -sfn "$old" "$previous"
	fi
	# ln -sfn replaces the existing symlink on both GNU/Linux and macOS. This
	# keeps the switch independent of GNU mv's non-portable -T option.
	ln -sfn "$release_dir" "$current"
	ln -sfn "$current/bin/go-agent-gateway" "$bin_dir/go-agent-gateway"
	ln -sfn "$current/bin/go-guest-agent" "$bin_dir/go-guest-agent"
	printf 'installed %s\n' "$VERSION"
	;;
rollback)
	current="$PREFIX/lib/go-agent-platform/current"
	previous="$PREFIX/lib/go-agent-platform/previous"
	[ -L "$current" ] && [ -L "$previous" ] || { printf '%s\n' 'no previous release is available' >&2; exit 1; }
	now=$(readlink "$current")
	old=$(readlink "$previous")
	ln -sfn "$old" "$current"
	ln -sfn "$now" "$previous"
	printf 'rolled back to %s\n' "$old"
	;;
current)
	current="$PREFIX/lib/go-agent-platform/current"
	[ -L "$current" ] || { printf '%s\n' 'no installed release' >&2; exit 1; }
	readlink "$current"
	;;
*) usage;;
esac
