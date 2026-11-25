#!/bin/sh
# After the package is removed (deb: remove/purge; rpm: 0 = erase). A deb
# purge also deletes the system service's state (its identity key,
# password and pairings in /var/lib/remohop), as a purge is meant to; a
# plain removal and the rpm keep it for a later reinstall.
set -e

case "${1:-}" in
remove | purge | 0)
	if [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; then
		systemctl daemon-reload >/dev/null 2>&1 || true
	fi
	;;
esac
if [ "${1:-}" = purge ]; then
	rm -rf /var/lib/remohop
fi
