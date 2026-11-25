#!/usr/bin/env bash
# Checks the signed macOS files that the CI repository returned to the release
# workflow against a rebuild from this checkout. Runs on Linux (and macOS).
#
# Usage: VERSION=v1.2.3 TEAM_ID=XXXXXXXXXX scripts/verify-macos-release.sh DIR
#   DIR holds remohop (universal, signed), RemoHop-macos.zip (the signed and
#   stapled RemoHop.app), RemoHop-macos.dmg and RemoHop-QuickSupport-macos.zip
#   (the signed and stapled "RemoHop Quick Support.app").
#
# The macOS binaries are built and signed in another repository, because the
# Apple credentials live there. That repository's word is not taken for what
# they contain: Go builds are reproducible (same source, Go version and flags
# give the same bytes on any host), so this script builds the same binaries
# from the tagged commit and requires (scripts/machocmp):
#   - remohop: each slice is byte for byte the rebuilt one plus an Apple code
#     signature of the expected team, identifier com.remohop.cli, hardened
#     runtime, no entitlements, page hashes matching the code;
#   - RemoHop.app (in the zip): the rebuilt bundle (packaging/quick/build.sh),
#     every file identical, the executable compared as above (com.remohop.quick),
#     plus only the signature and the stapled ticket;
#   - "RemoHop Quick Support.app" (in RemoHop-QuickSupport-macos.zip): likewise,
#     identifier com.remohop.quicksupport (the same program under the name that
#     starts quick support);
#   - RemoHop-macos.dmg: holds exactly that RemoHop.app, byte for byte, and
#     the /Applications link (read with 7-Zip, which understands HFS+ images).
# Whether Apple's certificate chain and notarization are valid is checked by
# codesign, spctl and stapler in the signing run, and again by Gatekeeper on
# every Mac that opens the files; what is checked here is that the signed code
# is this commit's code.
#
# Environment: VERSION (the tag; it is stamped into the binaries), TEAM_ID
# (Apple team ID of the Developer ID certificate), GO (default go).
set -euo pipefail
cd "$(dirname "$0")/.."

dir="${1:?usage: VERSION=vX.Y.Z TEAM_ID=ID scripts/verify-macos-release.sh DIR}"
VERSION="${VERSION:?set VERSION to the release tag}"
TEAM_ID="${TEAM_ID:?set TEAM_ID to the Apple team ID}"
GO="${GO:-go}"
[[ "$TEAM_ID" =~ ^[A-Z0-9]{10}$ ]] || { echo "verify-macos-release.sh: TEAM_ID must be a 10-character Apple team ID" >&2; exit 2; }
for f in remohop RemoHop-macos.zip RemoHop-macos.dmg RemoHop-QuickSupport-macos.zip; do
	[[ -f "$dir/$f" ]] || { echo "verify-macos-release.sh: $dir/$f is missing" >&2; exit 1; }
done
command -v 7z >/dev/null 2>&1 || { echo "verify-macos-release.sh: needs 7z (apt-get install 7zip, or p7zip-full)" >&2; exit 1; }

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fail() { echo "::error::$*" >&2; exit 1; }

# 1. The rebuild: the same commands as the signing workflow (and the Makefile).
echo "Rebuilding the darwin binaries of $VERSION"
make -s cross GO="$GO" VERSION="$VERSION" DIST="$tmp/ref" CROSS_TARGETS="darwin/arm64 darwin/amd64" >/dev/null
VERSION="$VERSION" OUT="$tmp/quick" TARGETS=macos GO="$GO" sh packaging/quick/build.sh >/dev/null
"$GO" build -o "$tmp/machocmp" ./scripts/machocmp
cmp=("$tmp/machocmp" -runtime -team "$TEAM_ID")

# 2. The command-line binary.
"${cmp[@]}" -id com.remohop.cli "$dir/remohop" "$tmp/ref/remohop-darwin-arm64" "$tmp/ref/remohop-darwin-amd64" ||
	fail "remohop is not the rebuilt binary with a Developer ID signature of team $TEAM_ID"

# 3. RemoHop.app from the zip. unzip refuses paths that leave the directory;
# machocmp refuses symlinks and any file the rebuild does not have.
mkdir "$tmp/zip"
unzip -q "$dir/RemoHop-macos.zip" -d "$tmp/zip"
[[ "$(ls -A "$tmp/zip")" == RemoHop.app ]] || fail "RemoHop-macos.zip holds more than RemoHop.app: $(ls -A "$tmp/zip" | tr '\n' ' ')"
"${cmp[@]}" -id com.remohop.quick -app "$tmp/zip/RemoHop.app" "$tmp/quick/macos/RemoHop.app" ||
	fail "RemoHop.app in RemoHop-macos.zip is not the rebuilt bundle with a Developer ID signature of team $TEAM_ID"

# 3b. "RemoHop Quick Support.app" from its zip.
mkdir "$tmp/qzip"
unzip -q "$dir/RemoHop-QuickSupport-macos.zip" -d "$tmp/qzip"
[[ "$(ls -A "$tmp/qzip")" == "RemoHop Quick Support.app" ]] || fail "RemoHop-QuickSupport-macos.zip holds more than RemoHop Quick Support.app: $(ls -A "$tmp/qzip" | tr '\n' ' ')"
"${cmp[@]}" -id com.remohop.quicksupport -app "$tmp/qzip/RemoHop Quick Support.app" "$tmp/quick/macos/RemoHop Quick Support.app" ||
	fail "RemoHop Quick Support.app in RemoHop-QuickSupport-macos.zip is not the rebuilt bundle with a Developer ID signature of team $TEAM_ID"

# 4. The disk image: list it, allow only the app, the /Applications link and
# the (empty) HFS+ metadata folders, then compare its app with the zip's.
7z l -slt "$dir/RemoHop-macos.dmg" >"$tmp/dmg.txt" || fail "7z cannot read RemoHop-macos.dmg"
awk '
	/^----------$/ { body = 1; next }
	!body { next }
	/^Path = / { path = substr($0, 8); next }
	/^Mode = / { mode = substr($0, 8); next }
	/^Alternate Stream = \+/ { print "alternate stream: " path; bad = 1; next }
	/^Method = / {
		if (path == "RemoHop" || path ~ /^RemoHop\/RemoHop\.app(\/|$)/) {
		} else if (path == "RemoHop/Applications" && mode ~ /^l/) {
			links++
		} else if ((path == "RemoHop/.HFS+ Private Directory Data\r" || path == "RemoHop/[HFS+ Private Data]") && mode ~ /^d/) {
			# HFS+ keeps hard-link data in these two folders (the first name really ends in CR).
		} else { print "unexpected entry: " path " (" mode ")"; bad = 1 }
		if (path ~ /^RemoHop\/RemoHop\.app/ && mode ~ /^l/) { print "symlink in the app: " path; bad = 1 }
		n++
	}
	END { if (n == 0 || links != 1) { print "no /Applications link or no entries"; bad = 1 } exit bad }
' "$tmp/dmg.txt" || fail "RemoHop-macos.dmg holds unexpected entries (above)"
# 7-Zip prints the target of a link as its content; the link must point at /Applications.
7z x -so "$dir/RemoHop-macos.dmg" RemoHop/Applications 2>/dev/null >"$tmp/link" || true
[[ "$(cat "$tmp/link")" == /Applications ]] || fail "the Applications link in RemoHop-macos.dmg does not point at /Applications"
7z x -y -o"$tmp/dmg" "$dir/RemoHop-macos.dmg" 'RemoHop/RemoHop.app' -r >/dev/null || fail "7z cannot unpack RemoHop.app from RemoHop-macos.dmg"
"$tmp/machocmp" -same "$tmp/dmg/RemoHop/RemoHop.app" "$tmp/zip/RemoHop.app" ||
	fail "RemoHop.app in RemoHop-macos.dmg differs from the one in RemoHop-macos.zip"

echo "macOS files of $VERSION: the rebuilt code, signed by team $TEAM_ID"
