#!/usr/bin/env bash
# Runs TestAccountsE2E against a real SMTP server: Mailpit in Docker, with
# STARTTLS required (a certificate from a throwaway CA) and SMTP
# authentication required (a random password). The rendezvous server trusts
# that CA through SSL_CERT_FILE and authenticates with --smtp-user and
# --smtp-password-file; login codes are read back through Mailpit's API.
#
# Needs docker, openssl, curl and go. Nothing secret is printed: the SMTP
# password exists only in files under a private temporary directory.
#
# Usage: test/e2e/accounts_mailpit.sh [/path/to/remohop]
set -uo pipefail
. "$(dirname "$0")/common.sh"

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
MAILPIT_IMAGE=${MAILPIT_IMAGE:-axllent/mailpit:v1.33.1}
WORK=$(mktemp -d)
chmod 700 "$WORK"
NAME=remohop-e2e-mailpit-$$
SMTP_PORT=$((21000 + RANDOM % 4000))
API_PORT=$((SMTP_PORT + 1))

cleanup() {
	docker rm -f "$NAME" >/dev/null 2>&1
	rm -rf "$WORK"
}
trap cleanup EXIT

for tool in docker openssl curl go; do
	command -v $tool >/dev/null || { echo "accounts_mailpit: $tool not found" >&2; exit 2; }
done

say "SMTP server: $MAILPIT_IMAGE, STARTTLS and AUTH required"
C=$WORK/certs
mkdir -p "$C"
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 2 -subj "/CN=RemoHop e2e SMTP CA" \
	-addext "basicConstraints=critical,CA:TRUE" -addext "keyUsage=critical,keyCertSign,cRLSign" \
	-keyout "$C/ca.key" -out "$C/ca.crt" 2>/dev/null
openssl req -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -subj "/CN=127.0.0.1" \
	-keyout "$C/server.key" -out "$C/server.csr" 2>/dev/null
printf 'subjectAltName=IP:127.0.0.1,DNS:localhost\nextendedKeyUsage=serverAuth\nkeyUsage=critical,digitalSignature\n' >"$C/ext"
openssl x509 -req -in "$C/server.csr" -CA "$C/ca.crt" -CAkey "$C/ca.key" -CAcreateserial -days 2 \
	-extfile "$C/ext" -out "$C/server.crt" 2>/dev/null
rm -f "$C/ca.key" "$C/server.csr"
SMTP_USER=remohop
SMTP_PASSWORD=$(openssl rand -hex 16)
printf '%s:%s\n' "$SMTP_USER" "$SMTP_PASSWORD" >"$C/auth"
# The container's user must read them; they are throwaway test material.
chmod 755 "$C"
chmod 644 "$C"/*
if openssl verify -CAfile "$C/ca.crt" "$C/server.crt" >/dev/null; then ok "test CA and server certificate (IP 127.0.0.1)"; else fail "certificate setup"; finish accounts; fi

docker pull -q "$MAILPIT_IMAGE" >/dev/null || { fail "docker pull $MAILPIT_IMAGE"; finish accounts; }
docker run -d --name "$NAME" -p "127.0.0.1:$SMTP_PORT:1025" -p "127.0.0.1:$API_PORT:8025" -v "$C:/certs:ro" \
	"$MAILPIT_IMAGE" --smtp-tls-cert /certs/server.crt --smtp-tls-key /certs/server.key \
	--smtp-require-starttls --smtp-auth-file /certs/auth >/dev/null || { fail "docker run mailpit"; finish accounts; }
if wait_until 60 "curl -fsS http://127.0.0.1:$API_PORT/api/v1/info"; then
	ok "Mailpit is up ($(curl -fsS "http://127.0.0.1:$API_PORT/api/v1/info" | grep -o '"Version":"[^"]*"'))"
else
	fail "Mailpit did not come up"; docker logs --tail 30 "$NAME" 2>&1 | sed 's/^/         /'; finish accounts
fi

# The server must really enforce what the test relies on: a mail that
# arrives proves remohop used STARTTLS and authenticated.
printf 'From: probe@example.com\r\nTo: probe@example.com\r\nSubject: probe\r\n\r\nprobe\r\n' >"$WORK/probe.eml"
probe() { curl -sS --max-time 20 "smtp://127.0.0.1:$SMTP_PORT" --mail-from probe@example.com --mail-rcpt probe@example.com -T "$WORK/probe.eml" "$@" >/dev/null 2>&1; }
if ! probe; then ok "Mailpit refuses a plaintext submission"; else fail "Mailpit accepted mail without STARTTLS"; fi
if ! probe --ssl-reqd --cacert "$C/ca.crt"; then ok "Mailpit refuses an unauthenticated submission over STARTTLS"; else fail "Mailpit accepted mail without AUTH"; fi
printf 'user = "%s:%s"\n' "$SMTP_USER" "$SMTP_PASSWORD" >"$WORK/curl-auth"
if probe --ssl-reqd --cacert "$C/ca.crt" -K "$WORK/curl-auth"; then ok "STARTTLS + AUTH submission works (curl)"; else fail "authenticated STARTTLS submission failed"; fi
rm -f "$WORK/curl-auth"

say "go test -run 'TestAccountsE2E|TestAccountsSMTPVerifiesTLS' ./test/e2e"
export REMOHOP_ACCOUNTS_E2E=1
export REMOHOP_E2E_MAILPIT=http://127.0.0.1:$API_PORT
export REMOHOP_E2E_SMTP_HOST=127.0.0.1 REMOHOP_E2E_SMTP_PORT=$SMTP_PORT
export REMOHOP_E2E_SMTP_USER=$SMTP_USER REMOHOP_E2E_SMTP_PASSWORD=$SMTP_PASSWORD
export REMOHOP_E2E_SMTP_CA=$C/ca.crt
if [ -n "${1:-}" ]; then REMOHOP_E2E_BIN=$(abspath "$1"); export REMOHOP_E2E_BIN; fi
if (cd "$ROOT" && go test -count=1 -v -run 'TestAccountsE2E|TestAccountsSMTPVerifiesTLS' ./test/e2e/ 2>&1) >"$WORK/test.log"; then
	grep -E '^(=== RUN|--- |    accounts_test|PASS|ok)' "$WORK/test.log" | cut -c1-300
	ok "account flows through Mailpit"
else
	cut -c1-300 "$WORK/test.log" | tail -n 80
	fail "account flows through Mailpit"
	echo "--- Mailpit log (tail)"
	docker logs --tail 30 "$NAME" 2>&1 | grep -v -i password | sed 's/^/         /'
fi
n=$(curl -fsS "http://127.0.0.1:$API_PORT/api/v1/messages?limit=1" | grep -o '"total":[0-9]*' | cut -d: -f2)
info "Mailpit received ${n:-?} mails in total (login codes are never printed)"

finish accounts
