#!/bin/sh
set -eu

if [ "$#" != 1 ]; then
	printf '%s\n' "usage: $0 SECRET_FILE < secret-value" >&2
	exit 2
fi
target=$1
case "$target" in
	/*) ;;
	*) printf '%s\n' 'secret path must be absolute' >&2; exit 2;;
esac
if [ -L "$target" ]; then
	printf '%s\n' 'refusing to replace a symlink secret path' >&2
	exit 2
fi
dir=$(dirname "$target")
mkdir -p "$dir"
umask 077
tmp=$(mktemp "$dir/.secret.XXXXXX")
trap 'rm -f "$tmp"' EXIT HUP INT TERM
cat > "$tmp"
bytes=$(wc -c < "$tmp" | tr -d ' ')
if [ "$bytes" -eq 0 ] || [ "$bytes" -gt 4096 ]; then
	printf '%s\n' 'secret must contain 1..4096 bytes' >&2
	exit 2
fi
chmod 0600 "$tmp"
mv -f "$tmp" "$target"
trap - EXIT HUP INT TERM
printf 'rotated %s\n' "$target"
