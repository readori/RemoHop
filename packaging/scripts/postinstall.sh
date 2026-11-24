#!/bin/sh
# Runs after the package is installed or upgraded. It enables nothing: a
# remote-control service should only exist because someone asked for it.
set -e

if [ -d /run/systemd/system ]; then
	systemctl daemon-reload >/dev/null 2>&1 || true
fi

cat <<'MSG'

RemoHop is installed. The host service is NOT enabled. To use it:

  per-user (as your own user, starts at login):
      systemctl --user enable --now remohop.service
      sudo loginctl enable-linger "$USER"      # optional: start at boot
  system-wide (starts at boot, state in /var/lib/remohop):
      sudo systemctl enable --now remohop.service

Then read the ID and password with `remohop status`
(system service: sudo env REMOHOP_CONFIG_DIR=/var/lib/remohop remohop status).

MSG
