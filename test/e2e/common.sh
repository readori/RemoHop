# Shared helpers of the test/e2e shell drivers (sourced, not run).
#
# "ok"/"FAIL" lines are assertions; "info" lines report and never fail.
# Output is meant for public CI logs: callers must not pass secrets to
# these helpers, and check/tail_of print only command output tails.

FAILED=0

abspath() { (cd "$(dirname "$1")" && printf '%s/%s\n' "$(pwd)" "$(basename "$1")"); }
say() { printf '\n== %s\n' "$*"; }
ok() { printf '   ok    %s\n' "$*"; }
fail() { printf '   FAIL  %s\n' "$*"; FAILED=1; }
info() { printf '   info  %s\n' "$*"; }
skip() { printf '   skip  %s\n' "$*"; }

# tail_of FILE [N]: the last N (15) lines of a file, indented, without
# lines that mention a password (a host prints its own).
tail_of() { grep -a -v -i 'password' "$1" 2>/dev/null | tail -n "${2:-15}" | cut -c1-300 | sed 's/^/         /'; }

# check DESCRIPTION COMMAND...: run, assert exit 0; output in $WORK/out.txt.
check() {
	local what=$1
	shift
	if "$@" >"$WORK/out.txt" 2>&1; then ok "$what"; else
		fail "$what (exit $?)"
		tail_of "$WORK/out.txt"
	fi
}

# wait_until SECONDS 'SHELL CONDITION': poll every 0.5 s.
wait_until() {
	local end=$((SECONDS + $1))
	while [ $SECONDS -lt $end ]; do
		eval "$2" >/dev/null 2>&1 && return 0
		sleep 0.5
	done
	eval "$2" >/dev/null 2>&1
}

# json_has TEXT LITERAL: TEXT contains LITERAL (for indented JSON output).
json_has() { grep -qF -- "$2" <<<"$1"; }

# finish NAME: print the verdict and exit with it.
finish() {
	echo
	if [ "$FAILED" = 0 ]; then echo "$1: PASS"; else echo "$1: FAIL"; exit 1; fi
}
