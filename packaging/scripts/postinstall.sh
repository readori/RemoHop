#!/bin/sh
# Runs after the package is installed or upgraded (deb: configure [OLD];
# rpm: 1 = install, 2 = upgrade; apk: no argument).
#
# A first install enables nothing: a remote-control service should only
# exist because someone asked for it. An upgrade restarts the host service
# if it is running, so that it runs the binary just installed, except when
# the upgrade itself runs in a RemoHop session (`remohop <ID> shell`): the
# restart would cut that shell off in the middle of the package manager's
# work, so it then says how to restart instead. Other remohop processes
# still running the old binary are listed.
set -e

upgrade=no
case "${1:-}" in
configure) [ -n "${2:-}" ] && upgrade=yes ;;
[0-9]*) [ "$1" -ge 2 ] && upgrade=yes ;;
esac

systemd=no
if [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; then
	systemd=yes
	systemctl daemon-reload >/dev/null 2>&1 || true
fi

# Is this script running under a remohop process (a RemoHop shell)?
inside_remohop() {
	pid=$$
	while [ "${pid:-1}" -gt 1 ] 2>/dev/null; do
		case "$(readlink "/proc/$pid/exe" 2>/dev/null || true)" in
		*/remohop | */remohop" (deleted)") return 0 ;;
		esac
		# The fields after "(comm) " are: state ppid ...
		pid=$(sed 's/.*) //' "/proc/$pid/stat" 2>/dev/null | cut -d' ' -f2)
	done
	return 1
}

# remohop processes that run a deleted (replaced) binary: "PID USER COMMAND".
stale() {
	for d in /proc/[0-9]*; do
		case "$(readlink "$d/exe" 2>/dev/null || true)" in
		*/remohop" (deleted)")
			user=$(stat -c %U "$d" 2>/dev/null || echo '?')
			cmd=$(tr '\0' ' ' <"$d/cmdline" 2>/dev/null | cut -c1-80)
			echo "  ${d#/proc/}  $user  $cmd"
			;;
		esac
	done
}

if [ "$upgrade" = yes ]; then
	if [ "$systemd" = yes ] && systemctl is-active --quiet remohop.service 2>/dev/null; then
		if inside_remohop; then
			cat <<'MSG'

remohop.service still runs the previous version. This upgrade runs inside a
RemoHop session, which restarting the service now would cut off. When it has
finished, restart it (that ends shells opened through it; background jobs
started with `remohop <ID> shell -b` keep running):
    sudo systemctl restart remohop.service

MSG
		elif systemctl try-restart remohop.service; then
			echo "remohop: restarted remohop.service to run the new version."
		else
			echo "remohop: could not restart remohop.service; run: sudo systemctl restart remohop.service" >&2
		fi
	fi
	left=$(stale)
	if [ -n "$left" ]; then
		cat <<MSG

These remohop processes still run the previous version (PID, user, command):
$left
Restart them to use the new one: a per-user service with
    remohop service restart        (or: systemctl --user restart remohop.service)
as that user; a host started by hand by stopping and starting it again.

MSG
	fi
	exit 0
fi

cat <<'MSG'

RemoHop is installed. The host service is NOT enabled. To use it:

  per-user (as your own user, starts at login):
      systemctl --user enable --now remohop.service
      sudo loginctl enable-linger "$USER"      # optional: start at boot
  system-wide (starts at boot, state in /var/lib/remohop):
      sudo systemctl enable --now remohop.service

`remohop service status` (and logs, restart, uninstall) then manages it.
Read its ID and password with `remohop status`
(system service: sudo env REMOHOP_CONFIG_DIR=/var/lib/remohop remohop status).
Later upgrades of this package restart a running system service.

MSG
