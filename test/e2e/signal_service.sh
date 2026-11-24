#!/usr/bin/env bash
# End-to-end test of the rendezvous server as a real, hardened systemd
# service (`remohop service install --role signal`), with a real host and a real
# client on this machine connecting through it by 9-digit ID, directly and
# through its relay.
#
# Needs: systemd as init, passwordless sudo, iptables (for the relay part).
# Run it as an ordinary user (the host and the client run as that user; the
# server runs as systemd's transient DynamicUser). It installs
# /usr/local/bin/remohop and the unit remohop-signal.service, uses UDP 21116-21117
# and 40000-40099, and removes the unit and its firewall rules on exit.
#
# Usage: test/e2e/signal_service.sh /path/to/remohop
#
# Forcing the relay on one machine: an iptables owner match drops every UDP
# packet this user's processes send to a port other than the rendezvous
# server's (21116, 21117) and its relay range (40000-40099). The host and
# the client then cannot reach each other directly, while the server (a
# different, transient user) can still answer them and relay between them.
set -uo pipefail
. "$(dirname "$0")/common.sh"

REMOHOP=$(abspath "${1:?usage: signal_service.sh /path/to/remohop}")
WORK=$(mktemp -d)
SYS_REMOHOP=/usr/local/bin/remohop
UNIT=/etc/systemd/system/remohop-signal.service
SIGNAL=127.0.0.1:21116
PASSWORD=svc-e2e-$RANDOM-$RANDOM
# The host binds a fixed port below the relay range and the ephemeral range,
# so it can never sit on a port the relay rule lets through.
HOST_PORT=$((20000 + RANDOM % 9000))
HOST_PID=
FW_RULES=()

cleanup() {
	local r
	for r in "${FW_RULES[@]}"; do sudo ${r/-I OUTPUT/-D OUTPUT} 2>/dev/null; done
	[ -z "$HOST_PID" ] || kill "$HOST_PID" 2>/dev/null
	if [ -f "$UNIT" ]; then sudo "$SYS_REMOHOP" service uninstall --role signal >/dev/null 2>&1; fi
	rm -rf "$WORK"
}
trap cleanup EXIT

[ -d /run/systemd/system ] || { echo "signal_service: systemd is not the init system here" >&2; exit 2; }
sudo -n true 2>/dev/null || { echo "signal_service: needs passwordless sudo" >&2; exit 2; }

host() { REMOHOP_CONFIG_DIR=$WORK/host "$REMOHOP" "$@"; }
client() { REMOHOP_CONFIG_DIR=$WORK/cli "$REMOHOP" "$@"; }
client_pw() { REMOHOP_CONFIG_DIR=$WORK/cli REMOHOP_PASSWORD=$PASSWORD "$REMOHOP" "$@"; }
journal() { sudo journalctl -u remohop-signal --no-pager -o cat "$@" 2>/dev/null; }
main_pid() { systemctl show -p MainPID --value remohop-signal 2>/dev/null; }

say "$("$REMOHOP" version) on $(uname -sr), $(systemctl --version | head -1)"
mkdir -p "$WORK/host" "$WORK/cli" "$WORK/other"

say "install"
# A binary in a home or temporary directory is hidden by the unit's sandbox
# (ProtectHome, PrivateTmp); install must say so instead of writing a unit
# that fails with 203/EXEC.
case "$REMOHOP" in
/home/* | /root/* | /tmp/* | /var/tmp/*)
	code=0
	sudo "$REMOHOP" service install --role signal >"$WORK/out.txt" 2>&1 || code=$?
	if [ "$code" != 0 ] && grep -q 'sandbox hides' "$WORK/out.txt" && [ ! -f "$UNIT" ]; then
		ok "refuses to install a binary the sandbox hides ($REMOHOP is under ${REMOHOP%/*})"
	else
		fail "install from a hidden path (exit $code)"; tail_of "$WORK/out.txt"
	fi
	;;
*) info "binary is outside home/tmp; the hidden-path refusal is not exercised" ;;
esac
sudo install -m 755 "$REMOHOP" "$SYS_REMOHOP"
check "sudo remohop service install --role signal --relay --relay-ports 40000-40099" \
	sudo "$SYS_REMOHOP" service install --role signal --relay --relay-ports 40000-40099
sed 's/^/         /' "$WORK/out.txt"

if wait_until 15 "systemctl is-active --quiet remohop-signal"; then ok "systemctl is-active remohop-signal"; else
	fail "the service is not active: $(systemctl is-active remohop-signal)"; journal -n 30 | sed 's/^/         /'
	exit 1
fi
check "remohop service status --role signal (exit 0 while running)" "$REMOHOP" service status --role signal
sed 's/^/         /' "$WORK/out.txt"
st=$("$REMOHOP" service status --role signal --json 2>/dev/null)
if json_has "$st" '"running": true' && json_has "$st" '"enabled": true' && json_has "$st" '"scope": "system"'; then
	ok "status --json: running, enabled at boot, system scope"
else
	fail "status --json: $st"
fi

say "hardening"
props=$(systemctl show remohop-signal -p DynamicUser -p ProtectSystem -p ProtectHome -p PrivateTmp -p NoNewPrivileges \
	-p MemoryMax -p MemoryDenyWriteExecute -p RestrictNamespaces -p CapabilityBoundingSet -p SystemCallFilter -p StateDirectory)
for want in DynamicUser=yes ProtectSystem=strict ProtectHome=yes PrivateTmp=yes NoNewPrivileges=yes \
	MemoryMax=536870912 MemoryDenyWriteExecute=yes StateDirectory=remohop-signal; do
	if grep -qx "$want" <<<"$props"; then ok "$want"; else fail "$want (systemd reports: $(grep "^${want%%=*}=" <<<"$props"))"; fi
done
if grep -q '^SystemCallFilter=.\+' <<<"$props"; then ok "SystemCallFilter is set (@system-service)"; else fail "no SystemCallFilter"; fi
if grep -qx 'CapabilityBoundingSet=' <<<"$props"; then ok "no capabilities"; else fail "$(grep CapabilityBoundingSet <<<"$props")"; fi
pid=$(main_pid)
user=$(ps -o user= -p "$pid" 2>/dev/null | tr -d ' ')
if [ -n "$user" ] && [ "$user" != root ]; then ok "the server runs as the transient user '$user', not root"; else fail "server user '$user' (pid $pid)"; fi
if ls /var/lib/remohop-signal/ >/dev/null 2>&1; then fail "the state directory is readable by $(id -un)"; else ok "the state directory is closed to other users"; fi
# The server sizes its connection limit to its own cgroup's MemoryMax, which
# lives on the service's cgroup, not on the root of /sys/fs/cgroup.
line=$(journal | grep -m1 'connection limit sized to memory')
if grep -q 'memory_mb=512\b' <<<"$line"; then ok "sized to MemoryMax: ${line#*msg=}"; else fail "memory sizing ignores MemoryMax: '${line#*msg=}'"; fi
score=$(systemd-analyze security remohop-signal --no-pager 2>/dev/null | tail -n 1)
[ -z "$score" ] || info "systemd-analyze security: $score"

say "server key"
if sudo "$SYS_REMOHOP" signal --show-key --data /var/lib/remohop-signal >"$WORK/key.txt" 2>&1; then
	KEY=$(sed -n 's/^server key: //p' "$WORK/key.txt")
fi
if [ -n "${KEY:-}" ]; then ok "sudo remohop signal --show-key --data /var/lib/remohop-signal"; else fail "show-key"; tail_of "$WORK/key.txt"; exit 1; fi
if journal | grep -qF "server key: $KEY"; then ok "it is the key the running service printed"; else fail "show-key and the service disagree"; fi

say "host and client through the service"
for c in host client; do
	$c config set signal "$SIGNAL" && $c config set signal-pin "$KEY" || fail "$c: remohop config set"
done
REMOHOP_CONFIG_DIR=$WORK/host "$REMOHOP" -v host --listen "0.0.0.0:$HOST_PORT" --password "$PASSWORD" >"$WORK/host.log" 2>&1 </dev/null &
HOST_PID=$!
if wait_until 20 "grep -q 'ID:' '$WORK/host.log'"; then
	ID=$(grep -m1 'ID:' "$WORK/host.log" | tr -dc 0-9)
	ok "host registered: ID $ID"
else
	fail "host did not register"; tail_of "$WORK/host.log"; exit 1
fi

client_pw "$ID" ping -c 2 >"$WORK/ping.txt" 2>&1
if grep -q 'reply from' "$WORK/ping.txt"; then ok "ping by ID ($(grep -m1 -o 'over [a-z]* path' "$WORK/ping.txt"))"; else fail "ping by ID"; tail_of "$WORK/ping.txt"; fi
code=0; out=$(client_pw "$ID" shell -c 'echo via-signal-service; exit 3' 2>/dev/null) || code=$?
if [ "$(tr -d '\r' <<<"$out")" = via-signal-service ] && [ "$code" = 3 ]; then ok "remohop <ID> shell -c: output and exit code"; else fail "shell -c (out='$out' code=$code)"; fi
code=0; REMOHOP_CONFIG_DIR=$WORK/cli REMOHOP_PASSWORD=wrong-password-1 "$REMOHOP" "$ID" shell -c true >"$WORK/out.txt" 2>&1 || code=$?
if [ "$code" != 0 ]; then ok "a wrong host password is refused (exit $code)"; else fail "wrong password accepted"; fi

# A client pinned to another server key must refuse this server.
OTHER=$("$REMOHOP" signal --show-key --data "$WORK/other" 2>/dev/null | sed -n 's/^server key: //p')
mkdir -p "$WORK/pin"
REMOHOP_CONFIG_DIR=$WORK/pin "$REMOHOP" config set signal "$SIGNAL"
REMOHOP_CONFIG_DIR=$WORK/pin "$REMOHOP" config set signal-pin "$OTHER"
code=0; REMOHOP_CONFIG_DIR=$WORK/pin REMOHOP_PASSWORD=$PASSWORD "$REMOHOP" "$ID" shell -c true >"$WORK/out.txt" 2>&1 || code=$?
if [ "$code" != 0 ]; then ok "a client pinned to a different server key refuses this server (exit $code)"; else fail "wrong signal-pin accepted"; fi

say "relay (direct UDP between host and client blocked)"
if command -v iptables >/dev/null; then
	uid=$(id -u)
	for t in iptables ip6tables; do
		command -v $t >/dev/null || continue
		r="$t -I OUTPUT -p udp -m owner --uid-owner $uid -m multiport ! --dports 53,21116,21117,40000:40099 -j DROP"
		if sudo $r; then FW_RULES+=("$r"); fi
	done
	relayed=
	for attempt in 1 2 3; do
		# A client socket that happens to land in 40000-40099 slips past
		# the rule; such an attempt connects directly and is retried.
		client_pw "$ID" ping -c 2 >"$WORK/relay.txt" 2>&1
		if grep -q 'over relayed path' "$WORK/relay.txt"; then relayed=1; break; fi
		info "attempt $attempt did not use the relay: $(grep -m1 -o 'over [a-z]* path' "$WORK/relay.txt" || tail -n 1 "$WORK/relay.txt")"
	done
	if [ -n "$relayed" ]; then
		ok "connected over the service's relay ($(grep -m1 -o '127.0.0.1:[0-9]*' "$WORK/relay.txt" | tail -n 1))"
		port=$(grep -m1 -o 'path [^ ]* -> 127.0.0.1:[0-9]*' "$WORK/relay.txt" | sed 's/.*://')
		if [ -n "$port" ] && [ "$port" -ge 40000 ] && [ "$port" -le 40099 ]; then ok "relay port $port is inside --relay-ports 40000-40099"; else fail "relay port '$port' outside 40000-40099"; fi
	else
		fail "no relayed connection"; tail_of "$WORK/relay.txt"
	fi
	code=0; out=$(client_pw "$ID" shell -c 'echo via-relay; exit 5' 2>/dev/null) || code=$?
	if [ "$(tr -d '\r' <<<"$out")" = via-relay ] && [ "$code" = 5 ]; then ok "shell -c through the relay"; else fail "shell -c through the relay (out='$out' code=$code)"; fi
	code=0; REMOHOP_RELAY=0 client_pw "$ID" ping -c 1 >"$WORK/out.txt" 2>&1 || code=$?
	if [ "$code" = 255 ] && grep -q 'relaying is turned off' "$WORK/out.txt"; then ok "a client with relays turned off refuses it"; else fail "REMOHOP_RELAY=0 (exit $code)"; tail_of "$WORK/out.txt"; fi
	if journal | grep -q 'relay session started'; then ok "the service logged the relay sessions"; else fail "no relay session in the service log"; fi
	for r in "${FW_RULES[@]}"; do sudo ${r/-I OUTPUT/-D OUTPUT}; done
	FW_RULES=()
else
	info "iptables not found: relay not exercised"
fi

say "logs, restart"
"$REMOHOP" service logs --role signal -n 200 >"$WORK/logs.txt" 2>&1
if ! grep -q 'rendezvous server listening' "$WORK/logs.txt"; then
	info "remohop service logs as $(id -un) shows no server lines (no journal access); trying with sudo"
	sudo "$SYS_REMOHOP" service logs --role signal -n 200 >"$WORK/logs.txt" 2>&1
fi
if grep -q 'rendezvous server listening' "$WORK/logs.txt"; then ok "remohop service logs --role signal"; else fail "service logs"; tail_of "$WORK/logs.txt"; fi

old_pid=$(main_pid)
check "sudo remohop service restart --role signal" sudo "$SYS_REMOHOP" service restart --role signal
if wait_until 15 "systemctl is-active --quiet remohop-signal && [ \"\$(systemctl show -p MainPID --value remohop-signal)\" != '$old_pid' ]"; then
	ok "active again with a new process ($old_pid -> $(main_pid))"
else
	fail "not back after restart: $(systemctl is-active remohop-signal)"
fi
key2=$(sudo "$SYS_REMOHOP" signal --show-key --data /var/lib/remohop-signal 2>/dev/null | sed -n 's/^server key: //p')
if [ "$key2" = "$KEY" ]; then ok "the key survived the restart (state directory persists)"; else fail "key changed across restart"; fi
# The host reconnects by itself and keeps its ID.
if wait_until 60 "REMOHOP_CONFIG_DIR='$WORK/host' '$REMOHOP' status --json 2>/dev/null | grep -q '\"online\": true' && REMOHOP_CONFIG_DIR='$WORK/host' '$REMOHOP' status --json | grep -q '\"id\": \"$ID\"'"; then
	ok "host back online with the same ID"
else
	fail "host did not come back with ID $ID"; REMOHOP_CONFIG_DIR=$WORK/host "$REMOHOP" status 2>&1 | grep -v -i password | sed 's/^/         /'
fi
code=0; out=$(client_pw "$ID" shell -c 'echo after-restart' 2>/dev/null) || code=$?
if [ "$(tr -d '\r' <<<"$out")" = after-restart ]; then ok "connect by the same ID after the restart"; else fail "after restart (out='$out' code=$code)"; fi
journal -p warning -n 20 >"$WORK/warn.txt"
if [ -s "$WORK/warn.txt" ]; then info "warnings in the service journal:"; sed 's/^/         /' "$WORK/warn.txt"; fi

say "uninstall"
kill "$HOST_PID" 2>/dev/null; HOST_PID=
check "sudo remohop service uninstall --role signal" sudo "$SYS_REMOHOP" service uninstall --role signal
if grep -q 'removed' "$WORK/out.txt"; then ok "uninstall says removed"; else fail "uninstall output: $(cat "$WORK/out.txt")"; fi
if [ ! -e "$UNIT" ]; then ok "$UNIT is gone"; else fail "$UNIT still exists"; fi
if ! systemctl is-active --quiet remohop-signal; then ok "not active"; else fail "still active"; fi
lf=$(systemctl show -p LoadState --value remohop-signal 2>/dev/null)
if [ "$lf" = not-found ]; then ok "systemd no longer knows the unit (LoadState=not-found)"; else fail "LoadState=$lf"; fi
code=0; "$REMOHOP" service status --role signal >"$WORK/out.txt" 2>&1 || code=$?
if [ "$code" != 0 ] && grep -q 'not installed' "$WORK/out.txt"; then ok "status: not installed"; else fail "status after uninstall (exit $code): $(cat "$WORK/out.txt")"; fi
if sudo "$SYS_REMOHOP" service uninstall --role signal 2>&1 | grep -q 'was not installed'; then ok "a second uninstall is a no-op"; else fail "second uninstall"; fi

finish signal_service
