#!/usr/bin/env bash
# End-to-end test of the real remohop binary through real Linux-kernel NATs.
#
# The host and the client each sit in their own network namespace behind
# their own NAT router. Each NAT has a provider router between it and the
# "internet" segment where the rendezvous server lives, so the two NATs are
# three hops apart, as real ones are:
#
#   sd-cli  192.168.2.2 -- sd-natB 100.65.2.2 -- sd-ispB 100.64.0.3 --+-- sd-inet 100.64.0.1
#   sd-host 192.168.1.2 -- sd-natA 100.65.1.2 -- sd-ispA 100.64.0.2 --+   (remohop signal)
#
# Neither LAN can reach the other, so a session only works if hole punching
# does. Needs root, iproute2 and iptables. It touches nothing outside the
# sd-* namespaces and removes them on exit; after every mode it checks that
# the root namespace (interfaces, routes, rules, firewall, forwarding,
# systemd-resolved's DNS settings, /etc/hosts, /etc/resolv.conf) is exactly
# as it was, since a process inside a namespace can still reach host-global
# state through shared files and the system bus (see root_snapshot).
#
# Usage: sudo test/natlab/natlab.sh [cone|symmetric|permissive|relay|wsrelay|vpn|mesh|tailnet|all]
#
#   cone        home-router NATs: MASQUERADE, unsolicited WAN input dropped.
#               Every feature must work over a punched path.
#   symmetric   a random source port per destination on both sides. Punching
#               cannot work; remohop must fail fast with the symmetric-NAT
#               diagnostic.
#   permissive  like cone, but the routers accept unsolicited WAN input, so a
#               probe that arrives early leaves a conntrack entry that makes
#               the router rewrite the source port of the reply. The host's
#               low-TTL priming probes are what gets through this.
#   vpn         a tunnel to a machine on the host's LAN and the host as internet exit;
#               routes, firewall rules and sysctls must be identical after it ends,
#               also after SIGKILL of either side (iptables and nft hosts). Not in all.
#   relay       symmetric NATs again, with `remohop signal --relay`: the session
#               must come up over the relay and say so, and a client that
#               turned relays off must still refuse.
#   wsrelay     the client's network drops all UDP but DNS and lets TCP out
#               only to the server's websocket bridge (`remohop signal
#               --ws-listen`), then only to an HTTP CONNECT proxy: remohop must
#               fall back to the bridge by itself and connect through the
#               relay. Then the host's UDP is blocked too (both on the
#               bridge), then only the host's (client on UDP).
#   mesh        `remohop net`: three devices of one account behind three NATs form
#               a mesh, reach each other by name, a signed policy blocks a port,
#               one device is subnet router and exit node; everything is gone
#               after `remohop net down`.
#   tailnet     a fake Tailscale (an interface named tailscale0 with 100.x
#               addresses joining host and client, and a stub status file):
#               the tailnet candidate must win over a faster punched path.
set -euo pipefail

MODE=${1:-all}
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
LAB=$(mktemp -d /tmp/remohop-natlab.XXXXXX)
REMOHOP=$LAB/remohop
NS=(sd-inet sd-ispA sd-ispB sd-natA sd-natB sd-host sd-cli sd-lan sd-ispC sd-natC sd-c3)
SIGNAL=100.64.0.1:21116
WSURL=ws://100.64.0.1:8080/remohop/v1/udp
PASSWORD=natlab-pass
FAILED=0

say()  { printf '\n== %s\n' "$*"; }
ok()   { printf '   ok    %s\n' "$*"; }
fail() { printf '   FAIL  %s\n' "$*"; FAILED=1; }

# Deleting a namespace does not end the processes inside it, so stop them
# first: the daemons, the test web server, anything a failed check left.
stop_all() {
	local n pids
	for n in "${NS[@]}"; do
		pids=$(ip netns pids "$n" 2>/dev/null || true)
		[ -z "$pids" ] || kill $pids 2>/dev/null || true
	done
	sleep 0.3
	for n in "${NS[@]}"; do
		pids=$(ip netns pids "$n" 2>/dev/null || true)
		[ -z "$pids" ] || kill -9 $pids 2>/dev/null || true
	done
}

teardown() {
	stop_all
	local n l
	for n in "${NS[@]}"; do
		ip netns del "$n" 2>/dev/null || true
		# resolv.conf and hosts files the mesh test gives its namespaces
		rm -f "/etc/netns/$n/resolv.conf" "/etc/netns/$n/hosts"; rmdir "/etc/netns/$n" 2>/dev/null || true
	done
	rmdir /etc/netns 2>/dev/null || true
	# A veth end that link() or test_tailnet created in the root namespace
	# and an interrupted run did not move yet. Only these exact names.
	for l in $(ip -o link show | awk -F': ' '{print $2}' | sed 's/@.*//' | grep -E '^[xy]-(sd-[A-Za-z0-9]+-[a-z0-9]+|ts)$' || true); do
		ip link del "$l" 2>/dev/null || true
	done
}
cleanup() { teardown; if [ -n "${NATLAB_KEEP:-}" ]; then echo "natlab: kept $LAB"; else rm -rf "$LAB"; fi; }
trap cleanup EXIT

[ "$(id -u)" = 0 ] || { echo "natlab: must run as root" >&2; exit 2; }
for tool in ip iptables go; do
	command -v "$tool" >/dev/null || { echo "natlab: $tool not found" >&2; exit 2; }
done

(cd "$ROOT" && CGO_ENABLED=0 go build -o "$REMOHOP" ./cmd/remohop)

# root_snapshot: the host-global state that nothing in the lab may change.
# Everything the lab does happens inside the sd-* namespaces, but some state
# is shared with the root namespace even from there: files under /etc (the
# hosts file, resolv.conf unless /etc/netns/NS has one), and services on the
# system bus. systemd-resolved is the dangerous one: `resolvectl dns remohop0`
# in a namespace sends the interface's index, which resolved applies to the
# root-namespace interface with that number (on a CI runner: index 2, eth0,
# the runner's uplink, which then loses DNS and with it GitHub).
root_snapshot() {
	echo '# interfaces'; ip -o link show | awk -F': ' '{print $2}' | sed 's/@.*//' | sort
	echo '# routes'; ip route show table all | grep -Ev '^(local|broadcast|multicast) |^(ff00|fe80)::|proto ra' | sort
	echo '# rules'; ip rule
	echo '# firewall'; iptables-save 2>/dev/null | grep -v '^#' | sed 's/\[[0-9]*:[0-9]*\]//' || true
	echo '# forwarding'; cat /proc/sys/net/ipv4/ip_forward
	if command -v resolvectl >/dev/null && resolvectl status >/dev/null 2>&1; then
		echo '# systemd-resolved'; resolvectl dns; resolvectl domain; resolvectl default-route 2>/dev/null || true
	fi
	echo '# /etc/hosts'; cat /etc/hosts
	echo '# /etc/resolv.conf'; cat /etc/resolv.conf 2>/dev/null || true
}
# Azure runners can hot-plug an accelerated-networking virtual function
# (enP<bus>s<slot>, a slave of eth0 with no address or routes of its own)
# at any time; it is not the lab's doing.
root_state() { root_snapshot | grep -Ev '\<enP[0-9]+s[0-9]+\>' || true; }
root_state >"$LAB/root.before"

root_same() { # mode
	root_state >"$LAB/root.after"
	if diff -u "$LAB/root.before" "$LAB/root.after" >"$LAB/root.diff"; then
		ok "$1: the root namespace and the host's DNS settings are exactly as before"
	else
		fail "$1: changed the root namespace or host-global files"; sed 's/^/         /' "$LAB/root.diff" | head -40
	fi
}
# The lab's namespaces have no way to whatever proxy this machine uses.
unset HTTP_PROXY HTTPS_PROXY ALL_PROXY NO_PROXY http_proxy https_proxy all_proxy no_proxy

link() { # nsA ifA addrA nsB ifB [addrB]: a veth pair between two namespaces
	ip link add "x-$1-$2" type veth peer name "y-$4-$5"
	ip link set "x-$1-$2" netns "$1" name "$2"
	ip link set "y-$4-$5" netns "$4" name "$5"
	ip -n "$1" addr add "$3" dev "$2"
	ip -n "$1" link set "$2" up
	[ -z "${6:-}" ] || ip -n "$4" addr add "$6" dev "$5"
	ip -n "$4" link set "$5" up
}

lab_up() { # mode
	teardown
	local n r
	for n in "${NS[@]}"; do ip netns add "$n"; ip -n "$n" link set lo up; done
	ip -n sd-inet link add br0 type bridge
	ip -n sd-inet addr add 100.64.0.1/24 dev br0
	ip -n sd-inet link set br0 up
	for r in sd-ispA sd-ispB sd-natA sd-natB; do
		ip netns exec $r sysctl -qw net.ipv4.ip_forward=1
	done
	# provider routers on the internet segment
	link sd-ispA upl 100.64.0.2/24 sd-inet pA; ip -n sd-inet link set pA master br0
	link sd-ispB upl 100.64.0.3/24 sd-inet pB; ip -n sd-inet link set pB master br0
	# NAT routers behind their providers
	link sd-natA wan 100.65.1.2/24 sd-ispA dwn 100.65.1.1/24
	link sd-natB wan 100.65.2.2/24 sd-ispB dwn 100.65.2.1/24
	ip -n sd-natA route add default via 100.65.1.1
	ip -n sd-natB route add default via 100.65.2.1
	ip -n sd-ispA route add 100.65.2.0/24 via 100.64.0.3
	ip -n sd-ispB route add 100.65.1.0/24 via 100.64.0.2
	ip -n sd-inet route add 100.65.1.0/24 via 100.64.0.2
	ip -n sd-inet route add 100.65.2.0/24 via 100.64.0.3
	# LANs
	link sd-natA lan 192.168.1.1/24 sd-host eth0 192.168.1.2/24
	link sd-natB lan 192.168.2.1/24 sd-cli eth0 192.168.2.2/24
	ip -n sd-host route add default via 192.168.1.1
	ip -n sd-cli route add default via 192.168.2.1
	for r in sd-natA sd-natB; do
		if [ "$1" = symmetric ]; then
			ip netns exec $r iptables -t nat -A POSTROUTING -o wan -j MASQUERADE --random-fully
		else
			ip netns exec $r iptables -t nat -A POSTROUTING -o wan -j MASQUERADE
		fi
		ip netns exec $r iptables -A FORWARD -i wan -o lan -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
		ip netns exec $r iptables -A FORWARD -i wan -o lan -j DROP
		if [ "$1" != permissive ]; then
			ip netns exec $r iptables -A INPUT -i wan -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
			ip netns exec $r iptables -A INPUT -i wan -j DROP
		fi
	done
}

# Run remohop as the client. `ip netns exec` gives each invocation a private mount
# namespace, so anything that must see a FUSE mount runs in one invocation.
cli()      { ip netns exec sd-cli env REMOHOP_CONFIG_DIR="$LAB/cfg-cli" REMOHOP_SIGNAL=$SIGNAL "$@"; }
remohop()      { cli "$REMOHOP" "$@"; }
remohop_pass() { cli env REMOHOP_PASSWORD=$PASSWORD "$REMOHOP" "$@"; }

start_services() {
	stop_all
	rm -rf "$LAB"/cfg-* "$LAB"/*.log "$LAB/work"
	mkdir -p "$LAB/cfg-signal" "$LAB/cfg-host" "$LAB/cfg-cli" "$LAB/work/remote" "$LAB/work/mnt"
	ip netns exec sd-inet env REMOHOP_CONFIG_DIR="$LAB/cfg-signal" \
		"$REMOHOP" signal --listen $SIGNAL --data "$LAB/cfg-signal" ${RELAY:+--relay} ${WS:+--ws-listen 100.64.0.1:8080} ${SIGNAL_FLAGS:-} >"$LAB/signal.log" 2>&1 </dev/null &
	sleep 1
	ip netns exec sd-host env REMOHOP_CONFIG_DIR="$LAB/cfg-host" ${HOST_WS:+REMOHOP_SIGNAL_WS=$WSURL} \
		"$REMOHOP" -v host --signal $SIGNAL --password $PASSWORD >"$LAB/host.log" 2>&1 </dev/null &
	local i
	for i in $(seq 1 100); do
		grep -q 'ID:' "$LAB/host.log" 2>/dev/null && break
		sleep 0.2
	done
	ID=$(grep -m1 'ID:' "$LAB/host.log" | tr -dc 0-9)
	[ -n "$ID" ] || { cat "$LAB/host.log"; echo "natlab: host did not register" >&2; exit 1; }
}

check() { # description command...
	local what=$1; shift
	if "$@" >"$LAB/out.txt" 2>&1; then ok "$what"; else fail "$what"; sed 's/^/         /' "$LAB/out.txt" | head -20; fi
}

test_cone() {
	say "cone NATs (home-router style): every feature over a punched path"
	lab_up cone; start_services
	local W=$LAB/work

	remohop_pass "$ID" ping -c 3 >"$LAB/ping.txt" 2>&1 || true
	if grep -q 'punched path' "$LAB/ping.txt"; then ok "ping over a punched path"; else fail "ping"; cat "$LAB/ping.txt"; fi

	check "pair" remohop_pass "$ID" pair --name lab
	check "passwordless reconnect by alias" remohop lab ping -c 1

	local out code=0
	out=$(remohop lab shell -c 'echo from-host; exit 7' 2>&1) || code=$?
	if [ "$out" = from-host ] && [ "$code" = 7 ]; then ok "shell -c output and exit code"; else fail "shell -c (out=$out code=$code)"; fi

	check "info" remohop lab info

	local job
	job=$(remohop lab spawn --name ticker 'for i in 1 2 3; do echo tick $i; sleep 0.2; done; exit 4' 2>/dev/null)
	code=0; out=$(remohop lab logs -f "$job" 2>&1) || code=$?
	if [ "$(echo "$out" | wc -l)" = 3 ] && [ "$code" = 4 ]; then ok "spawn + logs -f with the job's exit code"; else fail "spawn/logs (code=$code)"; fi

	echo served-from-host-lan >"$W/remote/index.html"
	ip netns exec sd-host python3 -m http.server 8080 --bind 127.0.0.1 --directory "$W/remote" >/dev/null 2>&1 </dev/null &
	sleep 1
	check "forward -b" remohop lab forward -b 18080:8080
	out=$(cli curl -s --max-time 5 http://127.0.0.1:18080/index.html || true)
	if [ "$out" = served-from-host-lan ]; then ok "HTTP through the forward"; else fail "forward data path (got '$out')"; fi
	check "stop background commands" remohop stop --all

	head -c 20000000 /dev/urandom >"$W/big.bin"
	check "cp upload" remohop lab cp "$W/big.bin" ":$W/remote/big.bin"
	check "cp download" remohop lab cp ":$W/remote/big.bin" "$W/back.bin"
	if cmp -s "$W/big.bin" "$W/back.bin" && cmp -s "$W/big.bin" "$W/remote/big.bin"; then ok "20 MB round trip is byte-identical"; else fail "cp integrity"; fi

	mkdir -p "$W/remote/proj/src"
	echo 'package main' >"$W/remote/proj/src/main.go"
	echo noise >"$W/remote/proj/debug.log"
	check "sync with an exclude" remohop lab sync --exclude '*.log' ":$W/remote/proj" "$W/proj"
	if [ -f "$W/proj/src/main.go" ] && [ ! -e "$W/proj/debug.log" ]; then ok "sync mirrored the tree without the excluded file"; else fail "sync result"; fi

	if [ -e /dev/fuse ]; then
		cat >"$LAB/mount.sh" <<-EOF
			set -e
			$REMOHOP lab mount :$W/remote/proj $W/mnt
			grep -q 'package main' $W/mnt/src/main.go
			echo edited-through-mount >>$W/mnt/src/main.go
			$REMOHOP umount $W/mnt
		EOF
		check "FUSE mount: read and write through the kernel" cli bash "$LAB/mount.sh"
		if grep -q edited-through-mount "$W/remote/proj/src/main.go"; then ok "the write reached the host"; else fail "mount write"; fi
	else
		echo "   skip  FUSE mount (/dev/fuse not available)"
	fi

	check "clip set" remohop lab clip set natlab-clipboard
	out=$(remohop lab clip get 2>/dev/null || true)
	if [ "$out" = natlab-clipboard ]; then ok "clip get"; else fail "clip get (got '$out')"; fi
}

test_symmetric() {
	say "symmetric NATs on both sides: must fail with a clear diagnostic"
	lab_up symmetric; start_services
	local start=$SECONDS code=0
	remohop_pass "$ID" ping -c 1 >"$LAB/sym.txt" 2>&1 || code=$?
	local took=$((SECONDS - start))
	if [ "$code" = 255 ] && grep -qi 'symmetric' "$LAB/sym.txt" && [ "$took" -le 4 ]; then
		ok "refused with the symmetric-NAT diagnostic after ${took}s"
	else
		fail "expected a fast exit 255 with a symmetric-NAT diagnostic (exit $code after ${took}s)"; cat "$LAB/sym.txt"
	fi
}

test_permissive() {
	say "NATs that accept unsolicited WAN input: punching relies on low-TTL priming"
	lab_up permissive
	local okc=0 i
	for i in 1 2 3 4 5; do
		start_services
		if remohop_pass "$ID" ping -c 1 >"$LAB/perm.txt" 2>&1; then okc=$((okc + 1)); fi
	done
	if [ "$okc" = 5 ]; then
		ok "punched 5 of 5 attempts"
	else
		fail "punched $okc of 5 attempts"; sed 's/^/         /' "$LAB/perm.txt" | head -3
	fi
}

test_relay() {
	say "symmetric NATs with the rendezvous server's relay enabled"
	lab_up symmetric
	RELAY=1 start_services
	local code=0
	remohop_pass "$ID" ping -c 2 >"$LAB/relay.txt" 2>&1 || code=$?
	if [ "$code" = 0 ] && grep -q 'relayed path' "$LAB/relay.txt"; then
		ok "connected over a relayed path, and says so"
	else
		fail "relayed ping (exit $code)"; cat "$LAB/relay.txt"
	fi
	local out; code=0
	out=$(remohop_pass "$ID" shell -c 'echo via-relay; exit 5' 2>/dev/null) || code=$?
	if [ "$out" = via-relay ] && [ "$code" = 5 ]; then ok "shell -c through the relay"; else fail "shell -c through the relay (out=$out code=$code)"; fi
	code=0
	cli env REMOHOP_RELAY=0 REMOHOP_PASSWORD=$PASSWORD "$REMOHOP" "$ID" ping -c 1 >"$LAB/relay-off.txt" 2>&1 || code=$?
	if [ "$code" = 255 ]; then ok "a client with relays turned off refuses it"; else fail "REMOHOP_RELAY=0 (exit $code)"; cat "$LAB/relay-off.txt"; fi

	say "public-server limits: one relayed session per address, monthly usage"
	RELAY=1 SIGNAL_FLAGS="--relay-per-source 1 --relay-kbps 2000 --relay-monthly-gb 1" start_services
	check "a long-lived relayed session (forward -b)" remohop_pass "$ID" forward -b 18081:22
	code=0; remohop_pass "$ID" ping -c 1 >"$LAB/persrc.txt" 2>&1 || code=$?
	if [ "$code" != 0 ] && grep -q 'already has 1 relayed session' "$LAB/persrc.txt"; then ok "a second session from the same address is refused and says why"; else fail "per-address cap (exit $code)"; cat "$LAB/persrc.txt"; fi
	check "stop the forward" remohop stop --all
	sleep 0.5
	check "the slot is free again as soon as the session closed" remohop_pass "$ID" ping -c 1
	if grep -q 'relay: on (.*1 per address.*relayed this month of 1.00 GB' "$LAB/signal.log"; then ok "the startup line shows the per-address cap and the budget"; else fail "startup line"; grep 'relay:' "$LAB/signal.log"; fi
	pids=$(ip netns pids sd-inet); kill $pids 2>/dev/null; sleep 1
	out=$(ip netns exec sd-inet "$REMOHOP" signal --status --data "$LAB/cfg-signal" --relay-monthly-gb 1 2>&1 || true)
	if echo "$out" | grep -q 'relay usage' && ! echo "$out" | grep -q '(0 bytes)'; then ok "remohop signal --status shows the bytes relayed this month"; else fail "--status"; echo "$out" | sed 's/^/         /'; fi

	say "cone NATs, but UDP between the peers blocked by the client's firewall: relay hint"
	lab_up cone
	RELAY=1 start_services
	ip netns exec sd-natB iptables -I FORWARD -i lan -p udp -d 100.65.1.0/24 -j DROP
	local t0=$SECONDS t1 t2
	code=0; remohop_pass "$ID" ping -c 1 >"$LAB/hint1.txt" 2>&1 || code=$?
	t1=$((SECONDS - t0))
	if [ "$code" = 0 ] && grep -q 'relayed path' "$LAB/hint1.txt"; then ok "first command: punch failed, relayed after ${t1}s"; else fail "first relayed ping (exit $code)"; cat "$LAB/hint1.txt"; fi
	t0=$SECONDS; code=0; remohop_pass "$ID" ping -c 1 >"$LAB/hint2.txt" 2>&1 || code=$?
	t2=$((SECONDS - t0))
	if [ "$code" = 0 ] && grep -q 'relayed path' "$LAB/hint2.txt" && [ "$t2" -le 3 ]; then
		ok "next command went to the relay at once (${t2}s instead of ${t1}s)"
	else fail "hinted ping (exit $code, ${t2}s)"; cat "$LAB/hint2.txt"; fi
}

# ---- websocket bridge ----------------------------------------------------
# block_udp ROUTER [DEST:PORT...]: the NAT router forwards no UDP from its
# LAN but DNS, and TCP only to the given destinations ("-" for none),
# dropping UDP silently the way a corporate firewall does. Without
# arguments it unblocks.
block_udp() {
	local r=$1 d; shift
	if ! ip netns exec "$r" iptables -L sdws -n >/dev/null 2>&1; then
		ip netns exec "$r" iptables -N sdws
		ip netns exec "$r" iptables -I FORWARD 1 -i lan -j sdws
	fi
	ip netns exec "$r" iptables -F sdws
	[ $# -gt 0 ] || return 0
	ip netns exec "$r" iptables -A sdws -p udp --dport 53 -j RETURN
	ip netns exec "$r" iptables -A sdws -p udp -j DROP
	for d in "$@"; do
		[ "$d" != - ] || continue
		ip netns exec "$r" iptables -A sdws -p tcp -d "${d%:*}" --dport "${d#*:}" -j RETURN
	done
	ip netns exec "$r" iptables -A sdws -p tcp -j REJECT --reject-with tcp-reset
}

# A minimal HTTP CONNECT proxy that logs every tunnel it opens.
write_proxy() {
	cat >"$LAB/proxy.py" <<-'EOF'
		import socket, sys, threading
		def pipe(a, b):
		    try:
		        while True:
		            d = a.recv(65536)
		            if not d: break
		            b.sendall(d)
		    except OSError: pass
		    finally:
		        for s in (a, b):
		            try: s.shutdown(socket.SHUT_RDWR)
		            except OSError: pass
		def handle(c):
		    f = c.makefile('rb')
		    line = f.readline().decode()
		    while f.readline() not in (b'\r\n', b'\n', b''): pass
		    method, target = line.split()[:2]
		    if method != 'CONNECT':
		        c.sendall(b'HTTP/1.1 405 Method Not Allowed\r\n\r\n'); c.close(); return
		    print('CONNECT', target, flush=True)
		    host, port = target.rsplit(':', 1)
		    u = socket.create_connection((host, int(port)))
		    c.sendall(b'HTTP/1.1 200 Connection established\r\n\r\n')
		    threading.Thread(target=pipe, args=(c, u), daemon=True).start()
		    pipe(u, c)
		ls = socket.socket(); ls.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
		ls.bind((sys.argv[1], int(sys.argv[2]))); ls.listen(64)
		while True:
		    c, _ = ls.accept()
		    threading.Thread(target=handle, args=(c,), daemon=True).start()
	EOF
}

ws_ping() { # description [env...]: a ping that must go over the websocket bridge
	local what=$1 code=0; shift
	cli env REMOHOP_SIGNAL_WS=$WSURL REMOHOP_PASSWORD=$PASSWORD "$@" "$REMOHOP" "$ID" ping -c 2 >"$LAB/ws.txt" 2>&1 || code=$?
	if [ "$code" = 0 ] && grep -q "${WANT_PATH:-relayed path (websocket)}" "$LAB/ws.txt"; then ok "$what"; else fail "$what (exit $code)"; sed 's/^/         /' "$LAB/ws.txt" | head -8; fi
}

test_wsrelay() {
	say "UDP blocked, only HTTP(S) out: the rendezvous server's websocket bridge"
	lab_up cone
	RELAY=1 WS=1 start_services
	local W=$LAB/work out code
	block_udp sd-natB 100.64.0.1:8080
	if cli timeout 3 "$REMOHOP" "$ID" ping -c 1 >/dev/null 2>&1; then fail "the client reached the host with UDP blocked and no bridge (lab broken)"; fi

	local t0=$SECONDS
	ws_ping "UDP blocked: fell back to the websocket bridge and connected through the relay"
	if [ $((SECONDS - t0)) -le 9 ]; then ok "the dial with the fallback took $((SECONDS - t0))s"; else fail "the dial with the fallback took $((SECONDS - t0))s"; fi
	out=$(cli env REMOHOP_SIGNAL_WS=$WSURL REMOHOP_PASSWORD=$PASSWORD "$REMOHOP" "$ID" shell -c 'echo via-ws; exit 6' 2>/dev/null) || code=$?
	if [ "$out" = via-ws ] && [ "${code:-0}" = 6 ]; then ok "shell -c over the bridge"; else fail "shell -c over the bridge (out=$out code=${code:-0})"; fi
	head -c 2000000 /dev/urandom >"$W/ws.bin"
	check "cp upload over the bridge" cli env REMOHOP_SIGNAL_WS=$WSURL REMOHOP_PASSWORD=$PASSWORD "$REMOHOP" "$ID" cp "$W/ws.bin" ":$W/remote/ws.bin"
	check "cp download over the bridge" cli env REMOHOP_SIGNAL_WS=$WSURL REMOHOP_PASSWORD=$PASSWORD "$REMOHOP" "$ID" cp ":$W/remote/ws.bin" "$W/ws.back"
	if cmp -s "$W/ws.bin" "$W/ws.back"; then ok "2 MB round trip over the bridge is byte-identical"; else fail "cp integrity over the bridge"; fi
	t0=$SECONDS
	WANT_PATH="relayed path (websocket)" ws_ping "REMOHOP_TRANSPORT=ws skips the UDP attempt" REMOHOP_TRANSPORT=ws
	if [ $((SECONDS - t0)) -le 3 ]; then ok "forced websocket dial took $((SECONDS - t0))s"; else fail "forced websocket dial took $((SECONDS - t0))s"; fi

	# Only a CONNECT proxy gets out.
	write_proxy
	ip netns exec sd-natB python3 "$LAB/proxy.py" 192.168.2.1 3128 >"$LAB/proxy.log" 2>&1 </dev/null &
	sleep 0.5
	block_udp sd-natB -
	ws_ping "only an HTTP CONNECT proxy gets out: through it to the bridge" HTTP_PROXY=http://192.168.2.1:3128 HTTPS_PROXY=http://192.168.2.1:3128
	if grep -q 'CONNECT 100.64.0.1:8080' "$LAB/proxy.log"; then ok "the proxy carried the websocket"; else fail "proxy not used"; cat "$LAB/proxy.log"; fi
	code=0
	cli env REMOHOP_SIGNAL_WS=$WSURL REMOHOP_PASSWORD=$PASSWORD REMOHOP_RELAY=0 HTTP_PROXY=http://192.168.2.1:3128 "$REMOHOP" "$ID" ping -c 1 >"$LAB/ws-norelay.txt" 2>&1 || code=$?
	if [ "$code" = 255 ] && grep -q 'relaying is turned off' "$LAB/ws-norelay.txt"; then ok "a client with relays turned off refuses the bridge's relay"; else fail "REMOHOP_RELAY=0 over the bridge (exit $code)"; cat "$LAB/ws-norelay.txt"; fi

	# The host's UDP is blocked too: both sides on the bridge.
	block_udp sd-natB 100.64.0.1:8080
	block_udp sd-natA 100.64.0.1:8080
	HOST_WS=1 RELAY=1 WS=1 start_services
	if grep -q 'websocket bridge' "$LAB/host.log"; then ok "a host with UDP blocked registered through the bridge"; else fail "host registration through the bridge"; tail -5 "$LAB/host.log"; fi
	ws_ping "both sides on the bridge"
	# Only the host's: a client on UDP reaches it through the relay.
	block_udp sd-natB
	WANT_PATH="relayed path" ws_ping "client on UDP to a host on the bridge"
	if host_status=$(ip netns exec sd-host env REMOHOP_CONFIG_DIR="$LAB/cfg-host" "$REMOHOP" host status 2>&1) && echo "$host_status" | grep -q 'websocket'; then ok "remohop host status says websocket"; else fail "host status"; echo "$host_status" | sed 's/^/         /'; fi
	if grep -q 'websocket bridge opened' "$LAB/signal.log"; then ok "the server logged the bridge connections"; else fail "signal log"; fi
	block_udp sd-natA
}

# ---- VPN ---------------------------------------------------------------
# A machine on a second LAN of the host (192.168.3.0/24) that the client has
# no way to reach except through `remohop <ID> vpn up --route`, and a web server
# on the internet segment whose access log shows where requests come from.
host_ns() { ip netns exec sd-host env REMOHOP_CONFIG_DIR="$LAB/cfg-host" "$@"; }

# snapshot NS: everything a tunnel may touch, in a form that compares.
snapshot() {
	ip -n "$1" -o link show | awk '{print $2}' | sort
	ip -n "$1" route show table all | sort
	ip -n "$1" rule | sort
	ip netns exec "$1" iptables-save 2>/dev/null | grep -v '^#' | sed 's/\[[0-9]*:[0-9]*\]//' || true
	ip netns exec "$1" nft list ruleset 2>/dev/null || true
	ip netns exec "$1" sh -c 'grep -H . /proc/sys/net/ipv4/conf/*/forwarding /proc/sys/net/ipv4/ip_forward'
}

same() { # description before-file ns
	snapshot "$3" >"$LAB/after.txt"
	if diff -u "$2" "$LAB/after.txt" >"$LAB/diff.txt"; then ok "$1"; else fail "$1"; sed 's/^/         /' "$LAB/diff.txt" | head -30; fi
}

wait_gone() { # ns [tenths]: wait for the tunnel interface to disappear
	local i
	for i in $(seq 1 "${2:-50}"); do ip -n "$1" -o link show | grep >/dev/null ' remohop[0-9]' || return 0; sleep 0.1; done
	return 1
}

vpn_round() { # firewall-backend
	local fw=$1 out code
	say "VPN ($fw on the host): tunnel, routed LAN, clean removal"
	lab_up cone
	ip netns add sd-lan 2>/dev/null || true; ip -n sd-lan link set lo up
	link sd-host eth1 192.168.3.1/24 sd-lan eth0 192.168.3.50/24
	export REMOHOP_VPN_FIREWALL=$fw
	start_services
	mkdir -p "$LAB/www-lan" "$LAB/www-inet"
	echo from-the-hosts-lan >"$LAB/www-lan/index.html"; echo from-the-internet >"$LAB/www-inet/index.html"
	ip netns exec sd-lan python3 -m http.server 8080 --bind 192.168.3.50 --directory "$LAB/www-lan" >"$LAB/lan-http.log" 2>&1 </dev/null &
	ip netns exec sd-inet python3 -m http.server 8081 --bind 100.64.0.1 --directory "$LAB/www-inet" >"$LAB/inet-http.log" 2>&1 </dev/null &
	sleep 0.7
	check "pair" remohop_pass "$ID" pair --name lab
	# A machine where iptables has ever been used has its (empty) built-in
	# chains; create them so that before and after compare like with like.
	for t in filter nat; do ip netns exec sd-host iptables -t $t -L -n >/dev/null; ip netns exec sd-host iptables -t $t -N natlab-x; ip netns exec sd-host iptables -t $t -X natlab-x; done
	ip netns exec sd-host iptables -A FORWARD -j ACCEPT; ip netns exec sd-host iptables -D FORWARD -j ACCEPT
	ip netns exec sd-host iptables -t nat -A POSTROUTING -j ACCEPT; ip netns exec sd-host iptables -t nat -D POSTROUTING -j ACCEPT
	snapshot sd-cli >"$LAB/cli.before"; snapshot sd-host >"$LAB/host.before"

	if cli curl -s --max-time 2 http://192.168.3.50:8080/ >/dev/null 2>&1; then fail "the host's LAN is reachable without a tunnel (lab broken)"; fi

	# --- plain tunnel plus a routed LAN, in the background
	check "vpn up --route 192.168.3.0/24 -b" remohop lab vpn up --route 192.168.3.0/24 -b
	sed 's/^/         /' "$LAB/out.txt"
	check "remohop vpn lists the tunnel" sh -c "ip netns exec sd-cli env REMOHOP_CONFIG_DIR=$LAB/cfg-cli $REMOHOP vpn | grep >/dev/null 'remohop0 10.77.0.2 -> 10.77.0.1'"
	check "ping the host over the tunnel (10.77.0.1)" cli python3 -c "
import socket,struct,os,time,sys
s=socket.socket(socket.AF_INET,socket.SOCK_RAW,socket.IPPROTO_ICMP); s.settimeout(2)
p=struct.pack('!BBHHH',8,0,0,os.getpid()&0xffff,1)+b'x'*32
c=sum(struct.unpack('!%dH'%(len(p)//2),p)); c=(c>>16)+(c&0xffff); c=~(c+(c>>16))&0xffff
s.sendto(p[:2]+struct.pack('!H',c)+p[4:],('10.77.0.1',0))
while True:
  d,a=s.recvfrom(200)
  if a[0]=='10.77.0.1' and d[20]==0: sys.exit(0)"
	out=$(cli curl -s --max-time 5 http://192.168.3.50:8080/ || true)
	if [ "$out" = from-the-hosts-lan ]; then ok "HTTP to 192.168.3.50 on the host's LAN through the tunnel"; else fail "HTTP through the tunnel (got '$out')"; fi
	if grep -q '^192.168.3.1 ' "$LAB/lan-http.log"; then ok "the LAN machine sees the host's address (masquerade)"; else fail "masquerade"; cat "$LAB/lan-http.log"; fi
	head -c 3000000 /dev/urandom >"$LAB/www-lan/big.bin"
	cli curl -s --max-time 20 -o "$LAB/big.out" http://192.168.3.50:8080/big.bin || true
	if cmp -s "$LAB/www-lan/big.bin" "$LAB/big.out"; then ok "3 MB over TCP through the tunnel is byte-identical (MSS clamp)"; else fail "bulk transfer through the tunnel"; fi
	if cli curl -s --max-time 2 http://100.64.0.1:8081/ >/dev/null && ! grep -q '100.65.1.2' "$LAB/inet-http.log"; then ok "traffic that was not routed stays off the tunnel"; else fail "unrouted traffic went through the tunnel"; fi
	# the LAN machine cannot open connections into the tunnel
	ip -n sd-lan route add 10.77.0.0/16 via 192.168.3.1
	out=$(ip netns exec sd-lan python3 -c "import socket;s=socket.socket();s.settimeout(1.5);s.connect(('10.77.0.2',22))" 2>&1 | tail -1 || true)
	if echo "$out" | grep >/dev/null 'timed out'; then ok "the host does not forward new connections into the tunnel"; else fail "LAN reached into the tunnel: $out"; ip netns exec sd-host iptables -S FORWARD -v 2>/dev/null | sed 's/^/         /'; fi
	check "a second vpn up to the same host is refused" sh -c "! ip netns exec sd-cli env REMOHOP_CONFIG_DIR=$LAB/cfg-cli REMOHOP_SIGNAL=$SIGNAL $REMOHOP lab vpn up"
	check "vpn down" remohop lab vpn down
	wait_gone sd-cli; wait_gone sd-host; sleep 0.3
	same "client: interface, routes and rules exactly as before" "$LAB/cli.before" sd-cli
	same "host: interface, routes, firewall and forwarding exactly as before" "$LAB/host.before" sd-host

	# --- the host refuses to be an exit until its owner says so
	code=0; remohop lab vpn up --exit >"$LAB/out.txt" 2>&1 || code=$?
	if [ "$code" != 0 ] && grep -q 'remohop config vpn-exit on' "$LAB/out.txt"; then ok "--exit refused while vpn-exit is off"; else fail "--exit with vpn-exit off (exit $code)"; cat "$LAB/out.txt"; fi
	code=0; remohop lab vpn up --route 100.64.0.0/24 >"$LAB/out.txt" 2>&1 || code=$?
	if [ "$code" != 0 ] && grep -q 'beyond the host' "$LAB/out.txt"; then ok "a route beyond the host's networks refused while vpn-exit is off"; else fail "non-local route (exit $code)"; cat "$LAB/out.txt"; fi
	host_ns "$REMOHOP" config feature vpn off
	code=0; remohop lab vpn up >"$LAB/out.txt" 2>&1 || code=$?
	if [ "$code" != 0 ] && grep -q 'feature "vpn"' "$LAB/out.txt"; then ok "vpn refused while the feature is off"; else fail "feature off (exit $code)"; cat "$LAB/out.txt"; fi
	host_ns "$REMOHOP" config feature vpn on
	same "client unchanged by the refusals" "$LAB/cli.before" sd-cli
	same "host unchanged by the refusals" "$LAB/host.before" sd-host

	# --- exit: all traffic through the host
	host_ns "$REMOHOP" config vpn-exit on
	: >"$LAB/inet-http.log"
	cli curl -s --max-time 3 http://100.64.0.1:8081/ >/dev/null
	check "vpn up --exit -b" remohop lab vpn up --exit -b
	sed 's/^/         /' "$LAB/out.txt"
	out=$(cli curl -s --max-time 5 http://100.64.0.1:8081/ || true)
	if [ "$out" = from-the-internet ] && [ "$(grep -c '^100.65.2.2 ' "$LAB/inet-http.log")" = 1 ] && [ "$(grep -c '^100.65.1.2 ' "$LAB/inet-http.log")" = 1 ]; then
		ok "internet server saw the client's NAT before and the HOST's NAT (100.65.1.2) with --exit"
	else fail "exit traffic"; cat "$LAB/inet-http.log"; fi
	check "the session itself is still up (shell over it)" remohop lab shell -c true
	if cli ip route get 192.168.2.1 | grep >/dev/null 'dev eth0'; then ok "the client's own LAN stays off the tunnel"; else fail "LAN route"; fi

	# --- kill -9 the client: nothing may stay, at the latest after the next remohop
	local pid
	pid=$(ip netns exec sd-cli env REMOHOP_CONFIG_DIR="$LAB/cfg-cli" "$REMOHOP" vpn --json | python3 -c 'import json,sys; print(json.load(sys.stdin)[0]["pid"])')
	kill -9 "$pid"; wait_gone sd-cli; local t0=$SECONDS
	if wait_gone sd-host 250; then ok "host removed its tunnel $((SECONDS - t0))s after the client was killed"; else fail "host tunnel still there 25s after the client died"; fi
	sleep 0.5
	same "host cleaned up by itself when the client was killed" "$LAB/host.before" sd-host
	grep -i 'vpn' "$LAB/host.log" | tail -6 | sed 's/^/         host.log: /'
	if cli ip route | grep >/dev/null '100.65.1.2'; then ok "(the killed client left its exception route, as expected)"; fi
	remohop vpn >/dev/null 2>&1 || true
	same "client: the next remohop start removed what the killed one left" "$LAB/cli.before" sd-cli

	# --- kill -9 the host while it forwards: the next host start cleans up
	check "vpn up --route again" remohop lab vpn up --route 192.168.3.0/24 -b
	cp "$LAB/host.log" "$LAB/host-before-kill.log" 2>/dev/null || true
	kill -9 $(ip netns pids sd-host) 2>/dev/null; sleep 0.5
	if snapshot sd-host | grep >/dev/null 'remohop'; then ok "(the killed host left its firewall rules, as expected)"; else fail "expected leftovers after kill -9 of the host"; grep -i vpn "$LAB/host-before-kill.log" | tail -5 | sed 's/^/         /'; fi
	t0=$SECONDS
	if wait_gone sd-cli 250; then ok "client removed its tunnel $((SECONDS - t0))s after the host was killed"; else fail "client tunnel still there 25s after the host died"; fi
	for i in $(seq 1 100); do ip netns exec sd-cli env REMOHOP_CONFIG_DIR="$LAB/cfg-cli" "$REMOHOP" vpn | grep >/dev/null 'no VPN' && break; sleep 0.2; done
	same "client cleaned up after losing the session" "$LAB/cli.before" sd-cli
	host_ns "$REMOHOP" vpn >/dev/null 2>&1 || true
	same "host: the next remohop start removed what the killed host left" "$LAB/host.before" sd-host
	unset REMOHOP_VPN_FIREWALL
}

test_vpn() { local fw; for fw in ${VPN_FW:-iptables nft}; do vpn_round "$fw"; done; }

# ---- Tailscale coexistence ---------------------------------------------
# A fake tailnet: a veth pair named tailscale0 straight between the host and
# the client, with 100.100.7.x addresses and a slow link, plus stub
# `tailscale status --json` files. The punched path through the NATs is
# faster, so only the preference for tailnet candidates makes the session
# take the tailnet.
tailnet_status() { # self-ip peer-ip
	cat <<-EOF
		{"Version": "0.0.0-natlab", "BackendState": "Running", "TailscaleIPs": ["$1"],
		 "Self": {"HostName": "self", "DNSName": "self.tail0000.ts.net.", "OS": "linux", "TailscaleIPs": ["$1"], "Online": true},
		 "MagicDNSSuffix": "tail0000.ts.net", "CurrentTailnet": {"Name": "natlab", "MagicDNSSuffix": "tail0000.ts.net", "MagicDNSEnabled": true},
		 "Peer": {"nodekey:1": {"HostName": "other", "DNSName": "other.tail0000.ts.net.", "OS": "linux", "TailscaleIPs": ["$2"], "Online": true}}}
	EOF
}

test_tailnet() {
	say "Tailscale: a shared tailnet is preferred over a punched path"
	lab_up cone
	ip link add x-ts type veth peer name y-ts
	ip link set x-ts netns sd-host name tailscale0
	ip link set y-ts netns sd-cli name tailscale0
	ip -n sd-host addr add 100.100.7.1 peer 100.100.7.2/32 dev tailscale0
	ip -n sd-cli addr add 100.100.7.2 peer 100.100.7.1/32 dev tailscale0
	ip -n sd-host link set tailscale0 up; ip -n sd-cli link set tailscale0 up
	# A slow tailnet (96 kbit/s, as through a congested relay): its QUIC
	# handshake takes a good half second, the punched path's milliseconds.
	ip netns exec sd-host tc qdisc add dev tailscale0 root tbf rate 96kbit burst 1600 latency 2s
	ip netns exec sd-cli tc qdisc add dev tailscale0 root tbf rate 96kbit burst 1600 latency 2s
	tailnet_status 100.100.7.1 100.100.7.2 >"$LAB/ts-host.json"
	tailnet_status 100.100.7.2 100.100.7.1 >"$LAB/ts-cli.json"
	export REMOHOP_TAILSCALE_STATUS=$LAB/ts-host.json
	start_services
	unset REMOHOP_TAILSCALE_STATUS
	local code=0 out
	cli env REMOHOP_TAILSCALE_STATUS="$LAB/ts-cli.json" REMOHOP_PASSWORD=$PASSWORD "$REMOHOP" "$ID" ping -c 3 >"$LAB/ts.txt" 2>&1 || code=$?
	if [ "$code" = 0 ] && grep -q 'tailnet path .* -> 100.100.7.1:' "$LAB/ts.txt"; then ok "the session took the tailnet path (100.100.7.1)"; else fail "tailnet path (exit $code)"; cat "$LAB/ts.txt"; fi
	if grep -q 'path=tailnet' "$LAB/host.log"; then ok "the host classifies the session as tailnet"; else fail "host path"; grep 'session opened' "$LAB/host.log" | tail -2; fi
	code=0
	cli env REMOHOP_TAILSCALE=off REMOHOP_PASSWORD=$PASSWORD "$REMOHOP" "$ID" ping -c 1 >"$LAB/ts-off.txt" 2>&1 || code=$?
	if [ "$code" = 0 ] && grep -q 'punched path' "$LAB/ts-off.txt"; then ok "without the preference (REMOHOP_TAILSCALE=off) the faster punched path wins"; else fail "control without preference (exit $code)"; cat "$LAB/ts-off.txt"; fi
	# Symmetric NATs now: punching is hopeless, the tailnet still works.
	for r in sd-natA sd-natB; do
		ip netns exec $r iptables -t nat -F POSTROUTING
		ip netns exec $r iptables -t nat -A POSTROUTING -o wan -j MASQUERADE --random-fully
	done
	REMOHOP_TAILSCALE_STATUS=$LAB/ts-host.json start_services
	code=0
	cli env REMOHOP_TAILSCALE_STATUS="$LAB/ts-cli.json" REMOHOP_PASSWORD=$PASSWORD "$REMOHOP" "$ID" ping -c 1 >"$LAB/ts-sym.txt" 2>&1 || code=$?
	if [ "$code" = 0 ] && grep -q 'tailnet path' "$LAB/ts-sym.txt"; then ok "symmetric NATs on both sides: connected over the tailnet anyway"; else fail "tailnet through symmetric NATs (exit $code)"; cat "$LAB/ts-sym.txt"; fi
	out=$(cli env REMOHOP_TAILSCALE_STATUS="$LAB/ts-cli.json" "$REMOHOP" doctor --offline 2>&1 || true)
	if echo "$out" | grep -q 'running.*found via REMOHOP_TAILSCALE_STATUS' && echo "$out" | grep -q '100.100.7.2 (tailnet)'; then ok "remohop doctor reports the tailnet"; else fail "remohop doctor"; echo "$out" | sed 's/^/         /'; fi
}

# ---- remohop net: a mesh of three devices of one account --------------------
# A third device (charlie) behind its own NAT; alpha is sd-host (with the
# LAN 192.168.3.0/24 behind it), bravo is sd-cli.
mesh_lab_up() {
	lab_up cone
	link sd-ispC upl 100.64.0.4/24 sd-inet pC; ip -n sd-inet link set pC master br0
	link sd-natC wan 100.65.3.2/24 sd-ispC dwn 100.65.3.1/24
	ip netns exec sd-ispC sysctl -qw net.ipv4.ip_forward=1
	ip netns exec sd-natC sysctl -qw net.ipv4.ip_forward=1
	ip -n sd-natC route add default via 100.65.3.1
	ip -n sd-ispC route add 100.65.1.0/24 via 100.64.0.2
	ip -n sd-ispC route add 100.65.2.0/24 via 100.64.0.3
	ip -n sd-ispA route add 100.65.3.0/24 via 100.64.0.4
	ip -n sd-ispB route add 100.65.3.0/24 via 100.64.0.4
	ip -n sd-inet route add 100.65.3.0/24 via 100.64.0.4
	link sd-natC lan 192.168.4.1/24 sd-c3 eth0 192.168.4.2/24
	ip -n sd-c3 route add default via 192.168.4.1
	ip netns exec sd-natC iptables -t nat -A POSTROUTING -o wan -j MASQUERADE
	ip netns exec sd-natC iptables -A FORWARD -i wan -o lan -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
	ip netns exec sd-natC iptables -A FORWARD -i wan -o lan -j DROP
	ip netns exec sd-natC iptables -A INPUT -i wan -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
	ip netns exec sd-natC iptables -A INPUT -i wan -j DROP
	link sd-host eth1 192.168.3.1/24 sd-lan eth0 192.168.3.50/24
	# A web server "on the internet" that is not the rendezvous server
	# (whose address stays off an exit route).
	ip -n sd-inet addr add 100.64.0.9/24 dev br0
}

ns_of() { case $1 in alpha) echo sd-host ;; bravo) echo sd-cli ;; charlie) echo sd-c3 ;; esac; }
# Each device gets its own hosts file, /etc/netns/NS/hosts (which `ip netns
# exec` also mounts over /etc/hosts there), so net-dns hosts never writes the
# machine's /etc/hosts. net-dns auto (the default, alpha and bravo) must leave
# systemd-resolved alone from inside a namespace: see root_snapshot.
dev() { local d=$1 ns; shift; ns=$(ns_of "$d"); ip netns exec "$ns" env REMOHOP_CONFIG_DIR="$LAB/cfg-$d" REMOHOP_SIGNAL=$SIGNAL REMOHOP_HOSTS_FILE="/etc/netns/$ns/hosts" "$@"; }
dremohop() { local d=$1; shift; dev "$d" "$REMOHOP" "$@"; }

# icmp_ping DEVICE NAME: resolve NAME there and ping it (raw ICMP) once a
# second, as ping does, until a reply comes or 10 s have passed. The first
# packet to a peer waits for the link, which takes a rendezvous and a punch.
icmp_ping() {
	dev "$1" python3 -c "
import socket,struct,os,sys,time
a=socket.gethostbyname(sys.argv[1])
s=socket.socket(socket.AF_INET,socket.SOCK_RAW,socket.IPPROTO_ICMP); s.settimeout(1)
start=time.time(); seq=0
while time.time()-start<float(sys.argv[2]):
  seq+=1
  p=struct.pack('!BBHHH',8,0,0,os.getpid()&0xffff,seq)+b'x'*32
  c=sum(struct.unpack('!%dH'%(len(p)//2),p)); c=(c>>16)+(c&0xffff); c=~(c+(c>>16))&0xffff
  s.sendto(p[:2]+struct.pack('!H',c)+p[4:],(a,0))
  try:
    while True:
      d,r=s.recvfrom(200)
      if r[0]==a and d[20]==0: print(a, 'seq', seq, 'after %.1fs'%(time.time()-start)); sys.exit(0)
  except socket.timeout: pass
sys.exit(1)" "$2" "${3:-10}"
}

net_json() { dremohop "$1" net status --json | python3 -c "import json,sys; print(json.load(sys.stdin).get('$2',''))"; }

wait_net() { # device: wait until its mesh is up
	local i
	for i in $(seq 1 75); do [ "$(net_json "$1" up 2>/dev/null)" = True ] && return 0; sleep 0.2; done
	return 1
}

login_code() { # device: ask for a login code and read it from the server's log
	dremohop "$1" login lab@example.com --send-only >/dev/null
	sleep 0.3
	grep -o 'code="[0-9 ]*"' "$LAB/signal.log" | tail -1 | tr -dc 0-9
}

test_mesh() {
	say "remohop net: three devices of one account behind three NATs"
	mesh_lab_up
	stop_all
	rm -rf "${LAB:?}"/cfg-* "${LAB:?}"/host-*.log
	mkdir -p "$LAB/cfg-signal"
	ip netns exec sd-inet env REMOHOP_CONFIG_DIR="$LAB/cfg-signal" "$REMOHOP" signal --listen $SIGNAL --data "$LAB/cfg-signal" \
		--accounts --accounts-dev-codes --dev-codes-on-public-address --account-domains example.com >"$LAB/signal.log" 2>&1 </dev/null &
	sleep 1
	local d i code fp afp out A B C
	declare -A HOSTPID
	for d in alpha bravo charlie; do
		mkdir -p "$LAB/cfg-$d" "/etc/netns/$(ns_of $d)"
		printf '127.0.0.1\tlocalhost\n' >"/etc/netns/$(ns_of $d)/hosts"
		dremohop $d config set name $d
		dev $d "$REMOHOP" -v host --signal $SIGNAL >"$LAB/host-$d.log" 2>&1 </dev/null &
		HOSTPID[$d]=$!
	done
	for d in alpha bravo charlie; do
		for i in $(seq 1 100); do grep -q 'ID:' "$LAB/host-$d.log" 2>/dev/null && break; sleep 0.2; done
		grep -q 'ID:' "$LAB/host-$d.log" || { fail "host $d did not register"; tail -5 "$LAB/host-$d.log"; return; }
	done
	ok "three hosts registered"

	# One account: alpha creates it, bravo and charlie join, alpha approves.
	code=$(login_code alpha)
	afp=$(dremohop alpha login lab@example.com --code "$code" --json | python3 -c 'import json,sys; print(json.load(sys.stdin)["account"])')
	for d in bravo charlie; do
		code=$(login_code $d)
		fp=$(dremohop $d login lab@example.com --code "$code" --account-fingerprint "$afp" --no-wait --json | python3 -c 'import json,sys; print(json.load(sys.stdin)["device"])')
		dremohop alpha account approve "$fp" >/dev/null
		dremohop $d account sync >/dev/null 2>&1 || true
	done
	dremohop bravo account sync >/dev/null 2>&1 || true
	if dremohop charlie account --json | grep -q '"bravo"'; then ok "one account with alpha, bravo and charlie"; else fail "account setup"; dremohop charlie account 2>&1 | head; return; fi

	# As in the VPN test: built-in chains exist once iptables has been used.
	for t in filter nat; do ip netns exec sd-host iptables -t $t -L -n >/dev/null; ip netns exec sd-host iptables -t $t -N natlab-x; ip netns exec sd-host iptables -t $t -X natlab-x; done
	ip netns exec sd-host iptables -A FORWARD -j ACCEPT; ip netns exec sd-host iptables -D FORWARD -j ACCEPT
	ip netns exec sd-host iptables -t nat -A POSTROUTING -j ACCEPT; ip netns exec sd-host iptables -t nat -D POSTROUTING -j ACCEPT
	for d in alpha bravo charlie; do snapshot "$(ns_of $d)" >"$LAB/$d.before"; done
	dremohop alpha net up >"$LAB/up-alpha.txt" 2>&1 || true
	dremohop bravo net up >"$LAB/up-bravo.txt" 2>&1 || true
	dremohop charlie net up --dns hosts >"$LAB/up-charlie.txt" 2>&1 || true
	for d in alpha bravo charlie; do
		if wait_net $d; then ok "$d: net up as $(net_json $d fqdn) $(net_json $d addr4)"; else fail "$d: net up"; cat "$LAB/up-$d.txt"; dremohop $d net status; return; fi
	done
	if pgrep -x systemd-resolve >/dev/null && command -v resolvectl >/dev/null; then
		if dremohop alpha net status | grep -q 'systemd-resolved left alone'; then ok "net-dns auto in a namespace: systemd-resolved (root namespace) left alone, and says so"; else fail "net-dns auto touched or did not report systemd-resolved"; dremohop alpha net status | sed 's/^/         /'; fi
	else
		echo "   skip  net-dns auto next to systemd-resolved (not running here; root_same still checks the rest)"
	fi
	for i in $(seq 1 50); do grep -q 'charlie.remohop' /etc/netns/sd-c3/hosts && grep -q 'alpha.remohop' /etc/netns/sd-c3/hosts && break; sleep 0.1; done
	if grep -q "^$(net_json alpha addr4)[[:space:]]alpha.remohop" /etc/netns/sd-c3/hosts; then ok "net-dns hosts: charlie's own hosts file has the mesh names"; else fail "net-dns hosts block"; sed 's/^/         /' /etc/netns/sd-c3/hosts; fi
	if dremohop bravo net peers | grep -q ' up '; then fail "links exist before any traffic"; else ok "no links before traffic (they come up on demand)"; fi
	A=$(net_json alpha addr4); B=$(net_json bravo addr4); C=$(net_json charlie addr4)
	case "$A $B $C" in "10.78."*" 10.78."*" 10.78."*) ok "addresses from 10.78.0.0/16: $A $B $C" ;; *) fail "addresses $A $B $C" ;; esac
	# Each device resolves through its own mesh responder.
	for d in alpha bravo charlie; do
		mkdir -p "/etc/netns/$(ns_of $d)"
		echo "nameserver $(net_json $d addr4)" >"/etc/netns/$(ns_of $d)/resolv.conf"
	done
	check "bravo pings charlie.remohop (bravo and charlie behind different NATs)" icmp_ping bravo charlie.remohop
	sed 's/^/         /' "$LAB/out.txt"
	check "charlie pings alpha.remohop" icmp_ping charlie alpha.remohop
	sed 's/^/         /' "$LAB/out.txt"
	check "alpha pings bravo.remohop" icmp_ping alpha bravo.remohop
	sed 's/^/         /' "$LAB/out.txt"
	if dremohop bravo net peers | grep -q 'charlie.remohop.*yes *up *punched'; then ok "remohop net peers: link to charlie up over a punched path"; else fail "net peers"; dremohop bravo net peers | sed 's/^/         /'; fi
	if grep -q 'client=net:' "$LAB/host-bravo.log" "$LAB/host-charlie.log" "$LAB/host-alpha.log"; then ok "the links' logins are limited to the mesh (net:NAME)"; else fail "scoped login"; grep 'session opened' "$LAB"/host-*.log | tail -3; fi
	code=0; dremohop bravo "$(grep -m1 'ID:' "$LAB/host-charlie.log" | tr -dc 0-9)" shell -c true >"$LAB/out.txt" 2>&1 || code=$?
	if [ "$code" != 0 ]; then ok "a mesh device still cannot open a shell on another (account-trust off)"; else fail "mesh member got a shell"; fi

	# A policy set on alpha reaches charlie, which enforces it.
	mkdir -p "$LAB/www-c"; echo mesh-web >"$LAB/www-c/index.html"
	dev charlie python3 -m http.server 8080 --bind "$C" --directory "$LAB/www-c" >/dev/null 2>&1 </dev/null &
	dev charlie python3 -m http.server 8090 --bind "$C" --directory "$LAB/www-c" >/dev/null 2>&1 </dev/null &
	sleep 0.7
	out=$(dev bravo curl -s --max-time 4 http://charlie.remohop:8090/ || true)
	if [ "$out" = mesh-web ]; then ok "before any policy: bravo reaches charlie.remohop:8090"; else fail "8090 before the policy (got '$out')"; fi
	cat >"$LAB/policy.json" <<-EOF
		{"version": 1, "rules": [{"from": ["*"], "to": ["*"], "ports": ["icmp", "8080/tcp", "53"]}]}
	EOF
	check "alpha: remohop net policy set" dremohop alpha net policy set "$LAB/policy.json"
	for i in $(seq 1 75); do [ "$(net_json charlie policy_version)" = 1 ] && break; sleep 0.2; done
	if [ "$(net_json charlie policy_version)" = 1 ]; then ok "charlie adopted alpha's signed policy"; else fail "policy distribution"; dremohop charlie net status; fi
	out=$(dev bravo curl -s --max-time 4 http://charlie.remohop:8080/ || true)
	if [ "$out" = mesh-web ]; then ok "the policy allows bravo -> charlie:8080"; else fail "8080 under the policy (got '$out')"; fi
	code=0; dev bravo curl -s --max-time 3 http://charlie.remohop:8090/ >/dev/null 2>&1 || code=$?
	if [ "$code" = 28 ]; then ok "the policy blocks bravo -> charlie:8090 (timed out)"; else fail "8090 not blocked (curl exit $code)"; fi
	check "ping is still allowed by the policy" icmp_ping bravo charlie.remohop
	check "bravo: remohop net policy reset" dremohop bravo net policy reset
	for i in $(seq 1 75); do [ "$(net_json charlie policy_version)" = 2 ] && break; sleep 0.2; done
	out=$(dev bravo curl -s --max-time 4 http://charlie.remohop:8090/ || true)
	if [ "$out" = mesh-web ]; then ok "after the reset (version 2, from bravo): 8090 open again"; else fail "8090 after reset (got '$out')"; fi

	# charlie's host is killed outright and started again: bravo notices the
	# dead link (keep-alives) and connects anew on the next packet.
	kill -9 "${HOSTPID[charlie]}"; wait "${HOSTPID[charlie]}" 2>/dev/null || true
	for i in $(seq 1 50); do ip -n sd-c3 -o link show | grep -q ' remohop[0-9]' || break; sleep 0.1; done
	dev charlie "$REMOHOP" -v host --signal $SIGNAL >>"$LAB/host-charlie.log" 2>&1 </dev/null &
	if wait_net charlie; then ok "charlie's host restarted with the mesh (net stays on)"; else fail "charlie mesh after restart"; fi
	check "bravo reaches charlie.remohop again after charlie was killed (reconnect)" icmp_ping bravo charlie.remohop 30
	sed 's/^/         /' "$LAB/out.txt"
	dev charlie python3 -m http.server 8080 --bind "$C" --directory "$LAB/www-c" >/dev/null 2>&1 </dev/null &

	# alpha: subnet router for 192.168.3.0/24 and exit node.
	mkdir -p "$LAB/www-lan" "$LAB/www-inet"
	echo from-the-hosts-lan >"$LAB/www-lan/index.html"; echo from-the-internet >"$LAB/www-inet/index.html"
	ip netns exec sd-lan python3 -m http.server 8080 --bind 192.168.3.50 --directory "$LAB/www-lan" >"$LAB/lan-http.log" 2>&1 </dev/null &
	ip netns exec sd-inet python3 -m http.server 8081 --bind 100.64.0.9 --directory "$LAB/www-inet" >"$LAB/inet-http.log" 2>&1 </dev/null &
	sleep 0.7
	if dev bravo curl -s --max-time 2 http://192.168.3.50:8080/ >/dev/null 2>&1; then fail "alpha's LAN reachable without routes (lab broken)"; fi
	check "alpha: net up --route 192.168.3.0/24 --exit" dremohop alpha net up --route 192.168.3.0/24 --exit
	check "bravo: net up --accept-routes" dremohop bravo net up --accept-routes
	check "charlie: net up --use-exit alpha" dremohop charlie net up --use-exit alpha
	for i in $(seq 1 75); do dremohop bravo net status | grep -q '192.168.3.0/24 via alpha' && break; sleep 0.2; done
	out=$(dev bravo curl -s --max-time 5 http://192.168.3.50:8080/ || true)
	if [ "$out" = from-the-hosts-lan ]; then ok "bravo reaches 192.168.3.50 on alpha's LAN through the mesh"; else fail "subnet route (got '$out')"; dremohop bravo net status | sed 's/^/         /'; fi
	if grep -q '^192.168.3.1 ' "$LAB/lan-http.log"; then ok "the LAN machine sees alpha's address (masquerade)"; else fail "masquerade"; cat "$LAB/lan-http.log"; fi
	for i in $(seq 1 75); do [ "$(net_json charlie exit_active)" = True ] && break; sleep 0.2; done
	out=$(dev charlie curl -s --max-time 5 http://100.64.0.9:8081/ || true)
	if [ "$out" = from-the-internet ] && grep -q '^100.65.1.2 ' "$LAB/inet-http.log"; then ok "charlie's internet traffic leaves through alpha (seen from alpha's NAT, 100.65.1.2)"; else fail "exit node (got '$out')"; cat "$LAB/inet-http.log"; dremohop charlie net status | sed 's/^/         /'; fi
	check "charlie still pings bravo.remohop with the exit route on" icmp_ping charlie bravo.remohop

	for d in alpha bravo charlie; do check "$d: net down" dremohop $d net down; done
	sleep 0.5
	if ! grep -q 'remohop' /etc/netns/sd-c3/hosts; then ok "net-dns hosts: net down removed the block"; else fail "hosts block left after net down"; sed 's/^/         /' /etc/netns/sd-c3/hosts; fi
	for d in alpha bravo charlie; do same "$d: interfaces, routes, firewall and forwarding exactly as before" "$LAB/$d.before" "$(ns_of $d)"; done
}

run() { # mode: the test, its namespaces gone, then the root namespace compared
	"test_$1"
	teardown
	root_same "$1"
}

case "$MODE" in
	cone|symmetric|permissive|relay|wsrelay|vpn|mesh|tailnet) run "$MODE" ;;
	all) for m in cone symmetric permissive relay wsrelay tailnet mesh; do run $m; done ;;
	*) echo "usage: $0 [cone|symmetric|permissive|relay|wsrelay|vpn|mesh|tailnet|all]" >&2; exit 2 ;;
esac

echo
if [ "$FAILED" = 0 ]; then echo "natlab: PASS"; else echo "natlab: FAIL"; exit 1; fi
