#!/bin/sh
# On package removal (not upgrade) stop the system service so the binary is
# not left running after it is deleted. User services are the user's own.
set -e

case "$1" in
remove | 0)
	if [ -d /run/systemd/system ]; then
		systemctl disable --now remohop.service >/dev/null 2>&1 || true
	fi
	;;
esac
