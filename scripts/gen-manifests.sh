#!/usr/bin/env bash
# Renders the package-manager manifests for one release from packaging/templates/,
# filling versions, URLs and SHA-256 values from the release's SHA256SUMS.
#
# Usage: scripts/gen-manifests.sh vX.Y.Z ASSETS_DIR OUT_DIR
#   ASSETS_DIR holds SHA256SUMS (as uploaded to the release); OUT_DIR gets:
#     homebrew-tap/Formula/remohop.rb                       -> the Homebrew tap repo (readori/homebrew-tap)
#     homebrew-tap/Casks/remohop-quick.rb                   -> the same tap: RemoHop.app (the desktop app)
#     scoop-bucket/bucket/remohop.json                      -> the Scoop bucket repo (readori/scoop-bucket)
#     winget/manifests/<letter>/<Publisher>/RemoHop/<ver>/*.yaml -> microsoft/winget-pkgs PR
#     aur/remohop-bin/{PKGBUILD,.SRCINFO}                   -> the AUR git repo
#     nix/remohop.nix
# Environment: REPO (default readori/RemoHop), RELEASE_DATE (default: today, UTC),
#   WINGET_PUBLISHER (the winget package id prefix; default RemoHop, so the id is RemoHop.RemoHop)
#   WINGET_PUBLISHER_NAME (display name; default RemoHop),
#   ALLOW_PRERELEASE=1 to render a prerelease (package managers should not track those).
#
# Values that need a human decision stay as REPLACE_ME... markers (today only the AUR
# maintainer). The script lists the ones left.
set -euo pipefail
cd "$(dirname "$0")/.."
TAG="${1:?usage: gen-manifests.sh vX.Y.Z ASSETS_DIR OUT_DIR}"; ASSETS="${2:?}"; OUT="${3:?}"
REPO="${REPO:-readori/RemoHop}"
[[ "$TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]] || { echo "bad tag $TAG" >&2; exit 2; }
if [[ "$TAG" == *-* && "${ALLOW_PRERELEASE:-}" != 1 ]]; then
	echo "gen-manifests.sh: $TAG is a prerelease; package managers should only get stable releases (ALLOW_PRERELEASE=1 to override)" >&2
	exit 3
fi
SUMS="$ASSETS/SHA256SUMS"; [[ -f "$SUMS" ]] || { echo "no $SUMS" >&2; exit 1; }
RELEASE_DATE="${RELEASE_DATE:-$(date -u +%Y-%m-%d)}"
WINGET_PUBLISHER="${WINGET_PUBLISHER:-RemoHop}"
WINGET_PUBLISHER_NAME="${WINGET_PUBLISHER_NAME:-RemoHop}"
[[ "$WINGET_PUBLISHER" =~ ^[A-Za-z0-9_]+$ ]] || { echo "WINGET_PUBLISHER must be letters, digits, underscore" >&2; exit 2; }

sum() { # asset name -> hex digest
	local h; h="$(awk -v f="$1" '$2 == f || $2 == "*" f { print $1; exit }' "$SUMS")"
	[[ "$h" =~ ^[0-9a-f]{64}$ ]] || { echo "SHA256SUMS has no valid entry for $1" >&2; exit 1; }
	echo "$h"
}
VERSION_NOV="${TAG#v}"
sedargs=(
	-e "s|@TAG@|$TAG|g" -e "s|@VERSION_NOV@|$VERSION_NOV|g" -e "s|@VERSION_AUR@|${VERSION_NOV//-/_}|g"
	-e "s|@WINGET_PUBLISHER@|$WINGET_PUBLISHER|g" -e "s|@WINGET_PUBLISHER_NAME@|$WINGET_PUBLISHER_NAME|g"
	-e "s|@REPO@|$REPO|g" -e "s|@OWNER@|${REPO%%/*}|g" -e "s|@RELEASE_DATE@|$RELEASE_DATE|g"
	-e "s|@SHA_LINUX_AMD64@|$(sum remohop_linux_amd64.tar.gz)|g"
	-e "s|@SHA_LINUX_ARM64@|$(sum remohop_linux_arm64.tar.gz)|g"
	-e "s|@SHA_DARWIN_UNIVERSAL@|$(sum remohop_darwin_universal.tar.gz)|g"
	-e "s|@SHA_WINDOWS_AMD64@|$(sum remohop_windows_amd64.zip)|g"
	-e "s|@SHA_WINDOWS_ARM64@|$(sum remohop_windows_arm64.zip)|g"
	-e "s|@SHA_WINDOWS_AMD64_UPPER@|$(sum remohop_windows_amd64.zip | tr a-f A-F)|g"
	-e "s|@SHA_WINDOWS_ARM64_UPPER@|$(sum remohop_windows_arm64.zip | tr a-f A-F)|g"
	-e "s|@SHA_MACOS_APP@|$(sum RemoHop-macos.zip)|g"
	-e "s|@SHA_MACOS_DMG@|$(sum RemoHop-macos.dmg)|g"
)
render() { # template out
	mkdir -p "$(dirname "$2")"
	sed "${sedargs[@]}" "packaging/templates/$1" >"$2"
	if grep -n '@[A-Z_0-9]*@' "$2" >&2; then echo "unfilled placeholder in $2" >&2; exit 1; fi
}

rm -rf "$OUT"
render homebrew/remohop.rb.in "$OUT/homebrew-tap/Formula/remohop.rb"
render homebrew/remohop-quick.rb.in "$OUT/homebrew-tap/Casks/remohop-quick.rb"
render scoop/remohop.json.in "$OUT/scoop-bucket/bucket/remohop.json"
letter="$(printf '%s' "$WINGET_PUBLISHER" | cut -c1 | tr "[:upper:]" "[:lower:]")"
w="$OUT/winget/manifests/$letter/$WINGET_PUBLISHER/RemoHop/$VERSION_NOV"
for f in RemoHop.RemoHop.yaml RemoHop.RemoHop.installer.yaml RemoHop.RemoHop.locale.en-US.yaml; do
	render "winget/$f.in" "$w/${f/#RemoHop./$WINGET_PUBLISHER.}"
done
render aur/PKGBUILD.in "$OUT/aur/remohop-bin/PKGBUILD"
render aur/SRCINFO.in "$OUT/aur/remohop-bin/.SRCINFO"
render nix/remohop.nix.in "$OUT/nix/remohop.nix"

echo "Rendered manifests for $TAG into $OUT. Still to decide (REPLACE_ME):"
grep -rhno 'REPLACE_ME[A-Za-z_]*\(([^)]*)\)\?' "$OUT" | sed 's/^[0-9]*://' | sort | uniq -c | sed 's/^/  /'
