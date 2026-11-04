#!/usr/bin/env bash
# Wayland integration (spec 13.3): headless sway with the libinput backend,
# PipeWire, xdg-desktop-portal with the wlr (Screenshot) and gtk (Access)
# backends, then TestWaylandScreenAndInput. Input goes through uinput, so
# this needs root (or write access to /dev/uinput), udev, and a host where
# new input devices reach the compositor: in a container run it
# --privileged --network host -v /dev:/dev.
# Packages: sway xdg-desktop-portal xdg-desktop-portal-wlr
# xdg-desktop-portal-gtk pipewire wev udev dbus.
set -euo pipefail
cd "$(dirname "$0")/../.."
# The runtime directory must exist before the session bus starts: portal
# backends are bus-activated and find the compositor socket through it.
export XDG_RUNTIME_DIR=${XDG_RUNTIME_DIR:-$(mktemp -d)}
chmod 700 "$XDG_RUNTIME_DIR"
if [[ -z "${DBUS_SESSION_BUS_ADDRESS:-}" ]]; then
	exec dbus-run-session -- "$0" "$@"
fi
work=$(mktemp -d)
pids=()
cleanup() { for p in "${pids[@]}"; do kill "$p" 2>/dev/null || true; done; rm -rf "$work"; }
trap cleanup EXIT

if ! pgrep -x systemd-udevd >/dev/null; then
	/lib/systemd/systemd-udevd --daemon
	udevadm trigger --action=add >/dev/null 2>&1 || true
	udevadm settle || true
fi
modprobe uinput 2>/dev/null || true

export WLR_BACKENDS=headless,libinput WLR_LIBINPUT_NO_DEVICES=1 WLR_HEADLESS_OUTPUTS=1 WLR_RENDERER=pixman
export LIBSEAT_BACKEND=${LIBSEAT_BACKEND:-noop} XDG_CURRENT_DESKTOP=sway XDG_SESSION_TYPE=wayland
sway -c test/integration/testdata/sway.conf >"$work/sway.log" 2>&1 &
pids+=($!)
for _ in $(seq 1 100); do [[ -S "$XDG_RUNTIME_DIR/wayland-1" ]] && break; sleep 0.1; done
[[ -S "$XDG_RUNTIME_DIR/wayland-1" ]] || { deploy/ci/annotate.sh "sway did not start" "$work/sway.log"; cat "$work/sway.log"; exit 1; }
export WAYLAND_DISPLAY=wayland-1
unset DISPLAY
pipewire >"$work/pipewire.log" 2>&1 &
pids+=($!)
sleep 0.5
dbus-update-activation-environment XDG_RUNTIME_DIR WAYLAND_DISPLAY XDG_CURRENT_DESKTOP XDG_SESSION_TYPE
/usr/libexec/xdg-desktop-portal >"$work/portal.log" 2>&1 &
pids+=($!)
sleep 2
if ! VM_AGENT_INTEGRATION=1 VM_AGENT_WAYLAND=1 deploy/ci/gotest.sh -count=1 -v -run 'TestWaylandScreenAndInput' ./test/integration/; then
	deploy/ci/annotate.sh "Wayland session logs" "$work/sway.log" "$work/portal.log" "$work/pipewire.log"
	exit 1
fi
