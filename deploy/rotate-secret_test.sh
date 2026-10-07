#!/bin/sh
set -eu
root=$(mktemp -d)
trap 'rm -rf "$root"' EXIT
target="$root/secret"
printf '%s\n' 'first-secret' | deploy/rotate-secret.sh "$target" >/dev/null
printf '%s\n' 'second-secret' | deploy/rotate-secret.sh "$target" >/dev/null
python3 - "$target" <<'PY'
import os, sys
path = sys.argv[1]
assert os.stat(path).st_mode & 0o777 == 0o600
assert open(path, 'rb').read() == b'second-secret\n'
PY
printf '%s\n' 'secret rotation rehearsal passed'
