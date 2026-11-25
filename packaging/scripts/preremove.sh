#!/bin/sh
# On package removal (not upgrade) stop the system service so the binary is
# not left running after it is deleted. That covers the packaged unit and
# one `remohop service install --system` wrote under the same name. User
# services belong to their users; the message says how to remove them.
set -e

case "${1:-}" in
remove | 0)
	if [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; then
		systemctl disable --now remohop.service >/dev/null 2>&1 || true
	fi
	cat <<'MSG'
remohop: the system service is stopped and disabled. A per-user service
(remohop service install --user, or systemctl --user enable remohop.service)
keeps trying to start a binary that no longer exists; its user should run
    systemctl --user disable --now remohop.service
and delete ~/.config/systemd/user/remohop.service if it is there.
MSG
	;;
esac
