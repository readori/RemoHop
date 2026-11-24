#!/usr/bin/env bash
# Loopback smoke test of a built remohop binary: one host and one client on this
# machine, connected directly (no rendezvous server, no NAT), exercising the
# commands that should behave the same on Linux, macOS and Windows (Git Bash).
#
# Usage: test/smoke/smoke.sh /path/to/remohop
#
# "ok"/"FAIL" lines are asserted. "info" lines report features that depend on
# the machine (a display, a clipboard) and never fail the run.
set -uo pipefail

REMOHOP=${1:?usage: smoke.sh /path/to/remohop}
REMOHOP=$(cd "$(dirname "$REMOHOP")" && pwd)/$(basename "$REMOHOP")
WORK=$(mktemp -d)
# Under Git Bash the binary is a native Windows program: give it (and
# everything else) one consistent Windows-style path instead of /tmp/...
if command -v cygpath >/dev/null 2>&1; then WORK=$(cygpath -m "$WORK"); fi
# Below every OS's ephemeral range (Linux starts at 32768), so an outgoing
# connection cannot already hold one of our ports.
PORT=$((20000 + RANDOM % 10000))
HTTP_PORT=$((PORT + 1))
TARGET=127.0.0.1:$PORT
PASSWORD=smoke-test-pw
FAILED=0
HOST_PID=
HTTP_PID=

ok()   { printf '   ok    %s\n' "$*"; }
fail() { printf '   FAIL  %s\n' "$*"; FAILED=1; }
info() { printf '   info  %s\n' "$*"; }

cleanup() {
	REMOHOP_CONFIG_DIR=$WORK/cli "$REMOHOP" stop --all >/dev/null 2>&1
	[ -z "$HTTP_PID" ] || kill "$HTTP_PID" 2>/dev/null
	[ -z "$HOST_PID" ] || kill "$HOST_PID" 2>/dev/null
	sleep 0.5
	rm -rf "$WORK"
}
trap cleanup EXIT

client() { REMOHOP_CONFIG_DIR=$WORK/cli "$REMOHOP" "$@"; }
check() { # description command...
	local what=$1; shift
	if "$@" >"$WORK/out.txt" 2>&1; then ok "$what"; else fail "$what"; sed 's/^/         /' "$WORK/out.txt" | head -15; fi
}

mkdir -p "$WORK/host" "$WORK/cli" "$WORK/remote" "$WORK/local"
echo "== $("$REMOHOP" version) on $(uname -s)"

REMOHOP_CONFIG_DIR=$WORK/host "$REMOHOP" host --no-signal --listen "$TARGET" --password "$PASSWORD" \
	>"$WORK/host.log" 2>&1 </dev/null &
HOST_PID=$!
for _ in $(seq 1 50); do
	grep -q 'host ready' "$WORK/host.log" 2>/dev/null && break
	sleep 0.2
done
if ! grep -q 'host ready' "$WORK/host.log"; then
	fail "host did not start"; cat "$WORK/host.log"; exit 1
fi
ok "host started"

REMOHOP_PASSWORD=$PASSWORD check "ping with the password" client "$TARGET" ping -c 2
REMOHOP_PASSWORD=$PASSWORD check "pair" client "$TARGET" pair --name smoke
check "passwordless reconnect by alias" client smoke ping -c 1
check "info" client smoke info
check "info --json" client smoke info --json

code=0; out=$(client smoke shell -c 'echo from-host' 2>/dev/null) || code=$?
out=$(printf '%s' "$out" | tr -d '\r')
if [ "$out" = from-host ] && [ "$code" = 0 ]; then ok "shell -c output"; else fail "shell -c output (out='$out' code=$code)"; fi
code=0; client smoke shell -c 'exit 7' >/dev/null 2>&1 || code=$?
if [ "$code" = 7 ]; then ok "shell -c exit code"; else fail "shell -c exit code (got $code)"; fi
code=0; out=$(printf 'piped-in' | client smoke shell -c 'cat' 2>/dev/null) || code=$?
if [ "$out" = piped-in ]; then ok "shell -c stdin"; else info "shell -c stdin not passed through a 'cat' on this host (out='$out')"; fi

job=$(client smoke spawn --name smoke-job 'echo job-line' 2>/dev/null | tr -d '\r')
if [ -n "$job" ]; then
	code=0; out=$(client smoke logs -f "$job" 2>/dev/null | tr -d '\r') || code=$?
	if [ "$out" = job-line ] && [ "$code" = 0 ]; then ok "spawn + logs -f"; else fail "spawn/logs (out='$out' code=$code)"; fi
	check "jobs --json" client smoke jobs --json
else
	fail "spawn printed no job id"
fi

head -c 5000000 /dev/urandom >"$WORK/local/big.bin"
check "cp upload" client smoke cp "$WORK/local/big.bin" ":$WORK/remote/big.bin"
check "cp download" client smoke cp ":$WORK/remote/big.bin" "$WORK/local/back.bin"
if cmp -s "$WORK/local/big.bin" "$WORK/local/back.bin" && cmp -s "$WORK/local/big.bin" "$WORK/remote/big.bin"; then
	ok "5 MB round trip is byte-identical"
else
	fail "cp integrity"
fi

mkdir -p "$WORK/remote/proj/src"
echo 'package main' >"$WORK/remote/proj/src/main.go"
echo noise >"$WORK/remote/proj/debug.log"
check "sync with an exclude" client smoke sync --exclude '*.log' ":$WORK/remote/proj" "$WORK/local/proj"
if [ -f "$WORK/local/proj/src/main.go" ] && [ ! -e "$WORK/local/proj/debug.log" ]; then
	ok "sync mirrored the tree without the excluded file"
else
	fail "sync result"
fi

PY=$(command -v python3 || command -v python || true)
if [ -n "$PY" ]; then
	echo served-by-host >"$WORK/remote/index.html"
	# Start the test web server and make sure it answers directly before
	# testing the forward; on a port it cannot bind (Windows reserves some
	# ranges) try another.
	web_ok=
	for _ in 1 2 3; do
		"$PY" -m http.server "$HTTP_PORT" --bind 127.0.0.1 --directory "$WORK/remote" >"$WORK/http.log" 2>&1 </dev/null &
		HTTP_PID=$!
		for _ in $(seq 1 20); do
			[ "$(curl -s --max-time 2 "http://127.0.0.1:$HTTP_PORT/index.html" | tr -d '\r')" = served-by-host ] && { web_ok=1; break; }
			kill -0 "$HTTP_PID" 2>/dev/null || break
			sleep 0.5
		done
		[ -n "$web_ok" ] && break
		info "test web server on port $HTTP_PORT did not answer: $(tail -n 1 "$WORK/http.log" | tr -d '\r')"
		kill "$HTTP_PID" 2>/dev/null
		HTTP_PORT=$((20000 + RANDOM % 10000))
	done
	[ -n "$web_ok" ] || fail "the test web server never answered"
	check "forward -b" client smoke forward -b "$((HTTP_PORT + 1)):$HTTP_PORT"
	# The test web server can take a moment to come up on a busy machine.
	out=
	for _ in 1 2 3 4 5; do
		out=$(curl -s --max-time 5 "http://127.0.0.1:$((HTTP_PORT + 1))/index.html" | tr -d '\r' || true)
		[ "$out" = served-by-host ] && break
		sleep 1
	done
	if [ "$out" = served-by-host ]; then ok "HTTP through the forward"; else fail "forward data path (got '$out')"; fi
	check "ps lists the forward" client ps
	check "stop --all" client stop --all
else
	info "python not found: forward not exercised"
fi

if client smoke clip set smoke-clipboard >"$WORK/out.txt" 2>&1; then
	out=$(client smoke clip get 2>/dev/null | tr -d '\r')
	if [ "$out" = smoke-clipboard ]; then ok "clip set/get ($(head -1 "$WORK/out.txt"))"; else fail "clip get returned '$out'"; fi
else
	info "clip set failed: $(head -1 "$WORK/out.txt")"
fi

if client smoke screenshot -o "$WORK/local/screen.png" >"$WORK/out.txt" 2>&1; then
	info "screenshot: $(sed "s#$WORK/local/##" "$WORK/out.txt" | head -1)"
else
	info "screenshot unavailable: $(head -1 "$WORK/out.txt" | cut -c1-160)"
fi

echo
if [ "$FAILED" = 0 ]; then echo "smoke: PASS"; else echo "smoke: FAIL"; echo "--- host log"; tail -20 "$WORK/host.log"; exit 1; fi
