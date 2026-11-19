# Deployment

vm-agent runs as the logged-in desktop user inside the interactive session
(spec 4.1). Scripts per platform:

| Task | Linux (X11) | Windows |
| --- | --- | --- |
| Install / upgrade | `linux/install.sh -b BINARY -v VERSION` | `windows/install.ps1 -Binary vm-agent.exe -Version VERSION` |
| Roll back | `linux/rollback.sh` | `windows/rollback.ps1` |
| Session preconditions (spec 4.2) | `linux/preconditions.sh`, then `sudo linux/preconditions.sh --system [--autologin USER] [--wayland]` | `windows/preconditions.ps1` (administrator) |
| Return an RDP session to the console | — | `windows/console-session.ps1` |
| Regression | `e2e.sh` | `windows-e2e.ps1` |

Installs keep one directory per release with `current` and `previous`
pointing at releases; configuration and state live outside them, so an
upgrade followed by a rollback keeps the configuration, the audit log, job
records and recordings (spec 13.2 acceptance 4).

Linux: a systemd user unit (`Restart=always`, `RestartSec=5`,
`After=graphical-session.target`, `KillMode=process` so `process/launch`
programs survive restarts). Configuration: `~/.config/vm-agent/vm-agent.env`;
state: `~/.local/state/vm-agent`. `linux/install_test.sh` rehearses the file
layout without systemd; `linux/systemd_test.sh` checks a real user manager.

Windows: a scheduled task triggered at the user's logon, plus a daily trigger
as a last resort, running `vm-agent.exe -supervise` with no task-level restart
(spec 4.1). Files under `%LOCALAPPDATA%\vm-agent`: `releases\`, the `current`
and `previous` junctions, `config\vm-agent.args` (arguments, kept across
upgrades) and `state\` (audit log, jobs, recordings, `vm-agent.lock`,
`vm-agent.log`).

Wayland sessions (spec 4.2, 13.3): run `sudo linux/preconditions.sh --system
--wayland` so the active session user can create the uinput device used for
input (the `uinput` module is loaded at boot and `/dev/uinput` gets the udev
`uaccess` tag, so only the user of the active local session can write it).

Where uinput cannot be granted, use the portal RemoteDesktop fallback. It
needs one approval by a person at the desktop; no script can give it:

1. Log in to the Wayland session as the desktop user.
2. In a terminal of that session run once, with the `-job-dir` from
   `VM_AGENT_ARGS` in `~/.config/vm-agent/vm-agent.env` (the token is stored
   in its parent directory; this is the install default):
   `VM_AGENT_PORTAL_SETUP=1 ~/.local/lib/vm-agent/current/vm-agent -job-dir ~/.local/state/vm-agent/jobs`
3. Approve the compositor's remote-desktop dialog within 5 minutes. The
   command prints where it stored the restore token
   (`wayland-remote-desktop.token` next to `vm-agent.lock`, mode 0600) and
   exits 0. It exits 1 if the dialog was refused or the compositor cannot
   persist the approval (it returned no restore token).
4. Restart the service (`systemctl --user restart vm-agent`). Input then goes
   through the portal whenever `/dev/uinput` is not writable. The portal
   hands out a new token on every use and vm-agent replaces the stored one;
   delete the file to revoke the approval, and repeat the steps if the
   compositor revokes it.

Without either channel `input/*` returns `unsupported` with a message naming
both options.

Recording uploads (optional): add `-upload-s3-endpoint host:port
-upload-s3-bucket NAME -upload-s3-access-key-file F -upload-s3-secret-key-file F`
(and `-upload-s3-prefix`, `-upload-s3-insecure` as needed) to the agent
arguments; key files are reread on every upload, so they can be rotated in
place.

Web remote desktop (optional, Windows only): add `-remote` to the agent
arguments and open `http://HOST:7070/remote/` in Chrome or Edge. It needs
ffmpeg and inbound UDP port 7071 (`-remote-udp-port`); the firewall rule must
allow UDP for the vm-agent.exe of the release in use. See
`docs/remote-desktop-design.md`.

With no active display (a closed laptop lid) the remote desktop captures at
about 16 frames a second. To get the usual rate, install the virtual display
driver once, as administrator, with the panel's resolution:
`windows/virtual-display.ps1 -Action install -Width 2560 -Height 1600`.
vm-agent then adds a virtual monitor while a session needs one and removes it
afterwards; no elevation is needed at run time. `-Action uninstall` removes
the driver and everything the script added. The driver is Amyuni's USB Mobile
Monitor Virtual Display (usbmmidd v2, copyright Amyuni Technologies Inc.); the
script downloads it from the vendor and checks its SHA-256 and its Microsoft
signature before installing.

A locked desktop cannot be shown or operated by vm-agent itself. To unlock
from the page, install the secure desktop service once, as administrator:
`windows/secure-desktop.ps1 -Action install -Binary <vm-agent.exe> -FFmpeg
<ffmpeg.exe>`. It copies both to `%ProgramFiles%\vm-agent` and runs a
LocalSystem service from there; run it again after upgrading vm-agent.
`-Action uninstall` removes the service and the copies. With the service
installed, anyone who can reach vm-agent's port can see the lock screen and
type into it, so use it only where that port is restricted
(`docs/security.md`).

Authentication and TLS are not implemented (spec D9): bind `-listen` to the
platform-facing interface and restrict the port at the network layer.
