#!/bin/sh
# Configure the desktop session preconditions of spec 4.2 on Linux X11:
# no screen lock, no screensaver or display power management, no sleep, and
# optionally automatic login into an X11 (not Wayland) GDM session.
#
#   deploy/linux/preconditions.sh            # as the desktop user
#   sudo deploy/linux/preconditions.sh --system [--autologin USER]
#
# The user part uses gsettings (GNOME), xset and autostart entries; the
# system part masks the sleep targets, edits the GDM configuration and
# enables lingering for the user's systemd instance. vm-agent's probe
# reports whether the result holds (session_locked, display.ready).
set -eu
system=0 autologin=
while [ $# -gt 0 ]; do
	case $1 in
	--system) system=1; shift ;;
	--autologin) autologin=$2; shift 2 ;;
	*) echo "usage: $0 [--system [--autologin USER]]" >&2; exit 2 ;;
	esac
done

if [ "$system" = 1 ]; then
	[ "$(id -u)" = 0 ] || { echo "preconditions: --system needs root" >&2; exit 2; }
	# Not sleeping (spec 4.2).
	systemctl mask sleep.target suspend.target hibernate.target hybrid-sleep.target
	for conf in /etc/gdm3/custom.conf /etc/gdm/custom.conf; do
		[ -f "$conf" ] || continue
		# X11 only in phase 1 and 2 (spec D6), plus optional auto login.
		python3 - "$conf" "$autologin" <<'PY'
import configparser, sys
path, user = sys.argv[1], sys.argv[2]
cfg = configparser.ConfigParser(strict=False, interpolation=None)
cfg.optionxform = str
cfg.read(path)
if not cfg.has_section("daemon"):
    cfg.add_section("daemon")
cfg.set("daemon", "WaylandEnable", "false")
if user:
    cfg.set("daemon", "AutomaticLoginEnable", "true")
    cfg.set("daemon", "AutomaticLogin", user)
with open(path, "w") as f:
    cfg.write(f, space_around_delimiters=False)
PY
		echo "preconditions: updated $conf"
	done
	if [ -n "$autologin" ]; then
		loginctl enable-linger "$autologin"
	fi
	echo "preconditions: system settings applied"
	exit 0
fi

[ "$(id -u)" != 0 ] || { echo "preconditions: run without --system as the desktop user" >&2; exit 2; }
if command -v gsettings >/dev/null 2>&1; then
	set_key() { gsettings set "$1" "$2" "$3" 2>/dev/null && echo "preconditions: $1 $2 = $3" || true; }
	set_key org.gnome.desktop.screensaver lock-enabled false
	set_key org.gnome.desktop.screensaver idle-activation-enabled false
	set_key org.gnome.desktop.session idle-delay 0
	set_key org.gnome.desktop.lockdown disable-lock-screen true
	set_key org.gnome.settings-daemon.plugins.power idle-dim false
	set_key org.gnome.settings-daemon.plugins.power sleep-inactive-ac-type "'nothing'"
	set_key org.gnome.settings-daemon.plugins.power sleep-inactive-battery-type "'nothing'"
fi
autostart=${XDG_CONFIG_HOME:-$HOME/.config}/autostart
mkdir -p "$autostart"
# xset at every login: no screensaver blanking and no DPMS.
cat >"$autostart/vm-agent-xset.desktop" <<DESK
[Desktop Entry]
Type=Application
Name=vm-agent: disable screensaver and DPMS
Exec=sh -c 'xset s off -dpms s noblank'
X-GNOME-Autostart-enabled=true
NoDisplay=true
DESK
# Hide screensaver and locker autostart entries shipped by the distribution.
for name in xscreensaver light-locker gnome-screensaver xfce4-screensaver mate-screensaver; do
	if [ -f "/etc/xdg/autostart/$name.desktop" ]; then
		printf '[Desktop Entry]\nHidden=true\n' >"$autostart/$name.desktop"
		echo "preconditions: disabled autostart of $name"
	fi
done
if [ -n "${DISPLAY:-}" ] && command -v xset >/dev/null 2>&1; then
	xset s off -dpms s noblank && echo "preconditions: xset applied to $DISPLAY"
fi
echo "preconditions: user settings applied; run with --system as root for sleep and login settings"
