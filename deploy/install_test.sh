#!/bin/sh
set -eu
root=$(mktemp -d)
trap 'rm -rf "$root"' EXIT
mkdir -p "$root/src"
printf '#!/bin/sh\nexit 0\n' > "$root/src/gateway"
printf '#!/bin/sh\nexit 0\n' > "$root/src/agent"
chmod 755 "$root/src/gateway" "$root/src/agent"
ROOT="$root/root" PREFIX=/usr/local VERSION=staging-1 deploy/install.sh "$root/src/gateway" "$root/src/agent" >/dev/null
[ -x "$root/root/usr/local/bin/go-agent-gateway" ]
[ -f "$root/root/etc/systemd/system/go-agent-gateway.service" ]
[ -d "$root/root/var/lib/go-agent-gateway/artifacts" ]
printf '%s\n' 'staged systemd install rehearsal passed'
