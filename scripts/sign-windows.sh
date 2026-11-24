#!/usr/bin/env bash
# Windows code-signing HOOK. Called by the release workflow between the build and the
# packaging, with the .exe files to sign. Today it does nothing: the binaries ship
# unsigned (SmartScreen warns on first run) until a signing service is set up.
#
# To enable signing, see packaging/README.md ("Windows signing"). The plan is SignPath
# Foundation (free for open source, needs the repository to be public): create the
# project there, then replace the body of `sign()` below with the submit-and-download
# step (or sign in the workflow with signpath/github-action-submit-signing-request and
# keep this script as the on/off switch). The hook is switched on by setting the
# repository variable WINDOWS_SIGNING to "signpath"; until `sign()` is implemented
# that makes the release FAIL on purpose, so nobody ships believing they are signed.
#
# Usage: scripts/sign-windows.sh FILE.exe...
set -euo pipefail
mode="${WINDOWS_SIGNING:-}"

sign() {
	# PLACEHOLDER(windows-signing): submit "$@" to the signing service, wait, and
	# overwrite each file with the signed one. Then verify, e.g. with osslsigncode verify.
	echo "sign-windows.sh: WINDOWS_SIGNING=$mode is set but signing is not implemented yet (see packaging/README.md)" >&2
	return 1
}

case "$mode" in
"") echo "sign-windows.sh: Windows signing is not configured; $# binaries stay unsigned" ;;
signpath) sign "$@" ;;
*) echo "sign-windows.sh: unknown WINDOWS_SIGNING=$mode" >&2; exit 1 ;;
esac
