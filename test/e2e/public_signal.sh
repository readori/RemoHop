#!/usr/bin/env bash
# End-to-end test against a real, public rendezvous server (remohop signal
# --relay on another machine): a host and a client on this machine find each
# other by 9-digit ID through it, then connect through its relay with direct
# UDP between them blocked.
#
# Usage: SIGNAL=HOST[:PORT] SIGNAL_PIN=KEY test/e2e/public_signal.sh /path/to/remohop
# Needs: outbound UDP to the server; passwordless sudo and iptables for the
# relay part (as on a GitHub-hosted runner).
#
# Forcing the relay: an iptables owner match drops this user's UDP except to
# the server's ports (21116-21117) and its relay range (40000-40099), so the
# host and client cannot reach each other directly but both reach the server.
# The host binds a fixed port outside those ranges.
set -uo pipefail
cd "$(dirname "$0")"
. ./common.sh

REMOHOP=$(abspath "${1:?usage: public_signal.sh /path/to/remohop}")
: "${SIGNAL:?set SIGNAL}" "${SIGNAL_PIN:?set SIGNAL_PIN}"
WORK=$(mktemp -d)
HOST_PORT=21190
PASSWORD=$(head -c 18 /dev/urandom | od -An -tx1 | tr -d ' \n')
HOST_PID=
FW_RULES=()
cleanup() {
	for r in "${FW_RULES[@]}"; do sudo ${r/ -I / -D } 2>/dev/null; done
	[ -n "$HOST_PID" ] && kill "$HOST_PID" 2>/dev/null
	wait 2>/dev/null
	rm -rf "$WORK"
}
trap cleanup EXIT

host() { REMOHOP_CONFIG_DIR=$WORK/host "$REMOHOP" "$@"; }
client() { REMOHOP_CONFIG_DIR=$WORK/cli "$REMOHOP" "$@"; }
client_pw() { REMOHOP_CONFIG_DIR=$WORK/cli REMOHOP_PASSWORD=$PASSWORD "$REMOHOP" "$@"; }

say "configure host and client for the public rendezvous server"
mkdir -p "$WORK/host" "$WORK/cli"
for c in host client; do
	$c config set signal "$SIGNAL" >/dev/null && $c config set signal-pin "$SIGNAL_PIN" >/dev/null || fail "$c: remohop config set"
done

say "host registers by ID"
host -v host --listen "0.0.0.0:$HOST_PORT" --password "$PASSWORD" >"$WORK/host.log" 2>&1 </dev/null &
HOST_PID=$!
if wait_until 40 "grep -q 'ID:' '$WORK/host.log'"; then
	ID=$(grep -m1 'ID:' "$WORK/host.log" | tr -dc 0-9)
	ok "host registered with the public server: ID $ID"
else
	fail "host did not register with $SIGNAL"
	tail_of "$WORK/host.log"
	finish public-signal
fi

client_pw "$ID" ping -c 2 >"$WORK/ping.txt" 2>&1
if grep -q 'reply from' "$WORK/ping.txt"; then ok "ping by ID ($(grep -m1 -o 'over [a-z]* path' "$WORK/ping.txt"))"; else fail "ping by ID"; tail_of "$WORK/ping.txt"; fi
code=0; out=$(client_pw "$ID" shell -c 'echo via-public-signal; exit 4' 2>/dev/null) || code=$?
if [ "$(tr -d '\r' <<<"$out")" = via-public-signal ] && [ "$code" = 4 ]; then ok "shell -c by ID: output and exit code"; else fail "shell -c (out='$out' code=$code)"; fi
code=0; REMOHOP_CONFIG_DIR=$WORK/bad REMOHOP_PASSWORD=wrong-password-1 "$REMOHOP" "$ID" ping -c 1 >"$WORK/out.txt" 2>&1 || code=$?
[ "$code" != 0 ] && ok "a wrong password is refused" || fail "wrong password accepted"

say "relay through the public server (direct UDP between host and client blocked)"
if command -v iptables >/dev/null && sudo -n true 2>/dev/null; then
	uid=$(id -u)
	for t in iptables ip6tables; do
		command -v $t >/dev/null || continue
		r="$t -I OUTPUT -p udp -m owner --uid-owner $uid -m multiport ! --dports 53,21116,21117,40000:40099 -j DROP"
		if sudo $r; then FW_RULES+=("$r"); fi
	done
	relayed=
	for attempt in 1 2 3 4; do
		# A client socket that lands in 40000-40099 slips past the rule and
		# connects directly; such an attempt is retried.
		client_pw "$ID" ping -c 3 >"$WORK/relay.txt" 2>&1
		if grep -q 'over relayed path' "$WORK/relay.txt"; then relayed=1; break; fi
		info "attempt $attempt did not use the relay: $(grep -m1 -o 'over [a-z]* path' "$WORK/relay.txt" || tail -n 1 "$WORK/relay.txt")"
	done
	if [ -n "$relayed" ]; then
		ok "connected over the public server's relay"
		ep=$(grep -m1 -oE 'path [^ ]+ -> [0-9.]+:[0-9]+' "$WORK/relay.txt" | sed 's/.*-> //')
		port=${ep##*:}
		info "relay endpoint: $ep"
		if [ -n "$port" ] && [ "$port" -ge 40000 ] && [ "$port" -le 40099 ]; then ok "relay port $port is inside 40000-40099"; else fail "relay port '$port' outside 40000-40099"; fi
		grep -q 'reply from' "$WORK/relay.txt" && ok "ping replies through the relay: $(grep -m1 -oE 'time=[0-9.]+ ?ms' "$WORK/relay.txt" || true)"
	else
		fail "no relayed connection"; tail_of "$WORK/relay.txt"
	fi
	code=0; out=$(client_pw "$ID" shell -c 'echo via-relay; exit 5' 2>/dev/null) || code=$?
	if [ "$(tr -d '\r' <<<"$out")" = via-relay ] && [ "$code" = 5 ]; then ok "shell -c through the relay"; else fail "shell -c through the relay (out='$out' code=$code)"; fi
	head -c $((1 << 20)) /dev/urandom >"$WORK/up.bin"
	mkdir -p "$WORK/remote" "$WORK/back"
	start=$SECONDS
	if client_pw "$ID" cp "$WORK/up.bin" ":$WORK/remote/up.bin" >"$WORK/out.txt" 2>&1 &&
		client_pw "$ID" cp ":$WORK/remote/up.bin" "$WORK/back/up.bin" >>"$WORK/out.txt" 2>&1 &&
		cmp -s "$WORK/up.bin" "$WORK/back/up.bin"; then
		ok "1 MiB up and down through the relay, byte-identical ($((SECONDS - start)) s)"
	else
		fail "cp through the relay"; tail_of "$WORK/out.txt"
	fi
	code=0; REMOHOP_RELAY=0 client_pw "$ID" ping -c 1 >"$WORK/out.txt" 2>&1 || code=$?
	if [ "$code" != 0 ]; then ok "a client with relays turned off refuses the relay (exit $code)"; else fail "REMOHOP_RELAY=0 still connected"; tail_of "$WORK/out.txt"; fi
else
	skip "iptables or passwordless sudo missing: relay not exercised"
fi

finish public-signal
