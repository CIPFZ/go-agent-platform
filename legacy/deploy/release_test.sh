#!/bin/sh
set -eu
root=$(mktemp -d)
trap 'rm -rf "$root"' EXIT
mkdir -p "$root/src"
printf '#!/bin/sh\necho gateway-one\n' > "$root/src/gateway-one"
printf '#!/bin/sh\necho agent-one\n' > "$root/src/agent-one"
printf '#!/bin/sh\necho gateway-two\n' > "$root/src/gateway-two"
printf '#!/bin/sh\necho agent-two\n' > "$root/src/agent-two"
chmod 755 "$root"/src/*
PREFIX="$root/prefix" "$PWD/deploy/release.sh" install one "$root/src/gateway-one" "$root/src/agent-one" >/dev/null
[ "$(PREFIX="$root/prefix" "$PWD/deploy/release.sh" current)" = "$root/prefix/lib/go-agent-platform/releases/one" ]
PREFIX="$root/prefix" "$PWD/deploy/release.sh" install two "$root/src/gateway-two" "$root/src/agent-two" >/dev/null
[ "$(PREFIX="$root/prefix" "$PWD/deploy/release.sh" current)" = "$root/prefix/lib/go-agent-platform/releases/two" ]
PREFIX="$root/prefix" "$PWD/deploy/release.sh" rollback >/dev/null
[ "$(PREFIX="$root/prefix" "$PWD/deploy/release.sh" current)" = "$root/prefix/lib/go-agent-platform/releases/one" ]
printf '%s\n' 'release install/rollback rehearsal passed'
