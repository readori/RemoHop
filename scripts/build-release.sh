#!/usr/bin/env bash
# Builds the release assets that do not need a macOS machine: linux and windows
# archives of remohop and of the quick-support program, .deb/.rpm/.apk packages and
# the SBOM. The macOS universal archive is made from the signed binary by
# `build-release.sh darwin PATH`, and the signed RemoHop.app zip is checked and
# copied by `build-release.sh darwin-app ZIP`. SHA256SUMS and the signature are
# made later (scripts/finalize-release.sh, then cosign in the release workflow).
#
# Usage:
#   VERSION=v1.2.3 scripts/build-release.sh build      # make cross + quick apps into dist/
#   VERSION=v1.2.3 scripts/build-release.sh package    # archives + packages into $OUT
#   VERSION=v1.2.3 scripts/build-release.sh darwin /path/to/signed/universal/remohop
#   VERSION=v1.2.3 scripts/build-release.sh darwin-app /path/to/RemoHop-macos.zip
#   VERSION=v1.2.3 scripts/build-release.sh all        # build + package
#
# Environment:
#   VERSION           release tag, vMAJOR.MINOR.PATCH[-prerelease] (required)
#   OUT               output directory (default: dist/release)
#   SOURCE_DATE_EPOCH timestamp stamped into archives (default: the commit time)
#   NFPM_VERSION      nfpm to use when it is not on PATH (installed with `go install`,
#                     which checks it against the Go checksum database)
#
# Asset names carry no version, so that .../releases/latest/download/NAME is a
# stable URL; the version is inside the archive (`remohop version`) and in the
# release tag. The distro packages carry the version in the name (nfpm's own).
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${VERSION:?set VERSION to the release tag, e.g. v1.2.3}"
OUT="${OUT:-dist/release}"
case "$OUT" in /*) ;; *) OUT="$PWD/$OUT" ;; esac
NFPM_VERSION="${NFPM_VERSION:-v2.41.3}"
case "$VERSION" in
v[0-9]*.[0-9]*.[0-9]*) ;;
*) echo "build-release.sh: VERSION must look like v1.2.3 (got $VERSION)" >&2; exit 2 ;;
esac
[[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]] || { echo "build-release.sh: bad VERSION $VERSION" >&2; exit 2; }

if [[ -z "${SOURCE_DATE_EPOCH:-}" ]]; then
	SOURCE_DATE_EPOCH="$(git log -1 --format=%ct 2>/dev/null || date +%s)"
fi
export SOURCE_DATE_EPOCH

LINUX_ARCHES="amd64 arm64"
WINDOWS_ARCHES="amd64 arm64"

# tar and zip of a staging directory with fixed owners, order and timestamps.
make_tar() { # out.tar.gz dir files...
	local out="$1" dir="$2"; shift 2
	tar --sort=name --owner=0 --group=0 --numeric-owner --mtime="@$SOURCE_DATE_EPOCH" \
		-C "$dir" -cf - "$@" | gzip -n -9 >"$out"
}
make_zip() { # out.zip dir files...
	local out="$1" dir="$2"; shift 2
	rm -f "$out"
	(cd "$dir" && TZ=UTC zip -q -X -9 "$out" "$@")
}

stage_common() { # dir
	cp LICENSE "$1/LICENSE"
	cp NOTICE "$1/NOTICE"
	cp "$LICENSES" "$1/THIRD_PARTY_LICENSES.txt"
	find "$1" -exec touch -d "@$SOURCE_DATE_EPOCH" {} +
}

step_build() {
	local targets=""
	for a in $LINUX_ARCHES; do targets="$targets linux/$a"; done
	for a in $WINDOWS_ARCHES; do targets="$targets windows/$a"; done
	# The darwin binaries are not released from here (the signed ones are); they are
	# built so that the SBOM lists the modules only the macOS build links.
	make cross VERSION="$VERSION" CROSS_TARGETS="${targets# } darwin/amd64 darwin/arm64"
	# Quick support (RemoHop.exe, remohop-quick): same flags, see packaging/quick.
	# RemoHop.app is built and signed on macOS by the signing workflow.
	VERSION="$VERSION" OUT=dist/quick TARGETS="windows linux" sh packaging/quick/build.sh
}

# Written once per run (package, darwin and darwin-app share it): a file left by an
# earlier run could describe other module versions.
licenses() {
	LICENSES="$PWD/dist/THIRD_PARTY_LICENSES.txt"
	mkdir -p dist
	if [[ "${LICENSES_FRESH:-}" != "$LICENSES" ]]; then
		packaging/third_party_licenses.sh >"$LICENSES.tmp"
		mv "$LICENSES.tmp" "$LICENSES"
		LICENSES_FRESH="$LICENSES"
	fi
}

nfpm_bin() {
	if command -v nfpm >/dev/null 2>&1; then command -v nfpm; return; fi
	local gb; gb="$(go env GOPATH)/bin/nfpm"
	[[ -x "$gb" ]] || go install "github.com/goreleaser/nfpm/v2/cmd/nfpm@$NFPM_VERSION" >&2
	echo "$gb"
}

step_package() {
	mkdir -p "$OUT"; licenses
	local a stage
	for a in $LINUX_ARCHES; do
		[[ -f "dist/remohop-linux-$a" ]] || { echo "missing dist/remohop-linux-$a (run the build step)" >&2; exit 1; }
		stage="$(mktemp -d)"
		cp "dist/remohop-linux-$a" "$stage/remohop"; chmod 755 "$stage/remohop"
		stage_common "$stage"
		make_tar "$OUT/remohop_linux_$a.tar.gz" "$stage" remohop LICENSE NOTICE THIRD_PARTY_LICENSES.txt
		rm -rf "$stage"
	done
	for a in $WINDOWS_ARCHES; do
		[[ -f "dist/remohop-windows-$a.exe" ]] || { echo "missing dist/remohop-windows-$a.exe" >&2; exit 1; }
		stage="$(mktemp -d)"
		cp "dist/remohop-windows-$a.exe" "$stage/remohop.exe"
		cp packaging/windows/README.txt "$stage/README.txt"
		stage_common "$stage"
		make_zip "$OUT/remohop_windows_$a.zip" "$stage" remohop.exe README.txt LICENSE NOTICE THIRD_PARTY_LICENSES.txt
		rm -rf "$stage"
	done

	# Quick support: the double-click program with the same license files.
	for a in $WINDOWS_ARCHES; do
		[[ -f "dist/quick/windows-$a/RemoHop.exe" ]] || { echo "missing dist/quick/windows-$a/RemoHop.exe (run the build step)" >&2; exit 1; }
		stage="$(mktemp -d)"
		cp "dist/quick/windows-$a/RemoHop.exe" "$stage/RemoHop.exe"
		stage_common "$stage"
		make_zip "$OUT/RemoHop-windows-$a.zip" "$stage" RemoHop.exe LICENSE NOTICE THIRD_PARTY_LICENSES.txt
		rm -rf "$stage"
	done
	for a in $LINUX_ARCHES; do
		[[ -f "dist/quick/linux-$a/remohop-quick" ]] || { echo "missing dist/quick/linux-$a/remohop-quick (run the build step)" >&2; exit 1; }
		stage="$(mktemp -d)"
		cp "dist/quick/linux-$a/remohop-quick" "$stage/remohop-quick"; chmod 755 "$stage/remohop-quick"
		stage_common "$stage"
		make_tar "$OUT/remohop-quick_linux_$a.tar.gz" "$stage" remohop-quick LICENSE NOTICE THIRD_PARTY_LICENSES.txt
		rm -rf "$stage"
	done

	# nfpm does not expand variables in file sources, so the binary is staged
	# where packaging/nfpm.yaml expects it (packaging/stage is git-ignored, and
	# removed again even when a package step fails).
	local nfpm; nfpm="$(nfpm_bin)"
	trap 'rm -rf packaging/stage' EXIT
	mkdir -p packaging/stage
	for a in $LINUX_ARCHES; do
		cp "dist/remohop-linux-$a" packaging/stage/remohop
		cp "$LICENSES" packaging/stage/THIRD_PARTY_LICENSES.txt
		for fmt in deb rpm apk; do
			GOARCH="$a" VERSION="${VERSION#v}" "$nfpm" package -f packaging/nfpm.yaml -p "$fmt" -t "$OUT"
		done
	done
	rm -rf packaging/stage
	# nfpm names prerelease packages with "~" (1.2.3~rc.1, so that they sort before
	# 1.2.3); GitHub would rename such an asset on upload, and SHA256SUMS would then
	# list a name the release does not have.
	local f
	for f in "$OUT"/*~*; do
		[[ -e "$f" ]] && mv "$f" "${f//\~/.}"
	done

	# SBOM: one for the release, from the binaries just built. The darwin builds stand
	# in for the signed macOS binary: the release workflow checks that it is exactly
	# this code (scripts/verify-macos-release.sh), and macOS links modules the other
	# systems do not (github.com/ebitengine/purego).
	[[ -f dist/remohop-darwin-arm64 && -f dist/remohop-darwin-amd64 ]] ||
		{ echo "missing dist/remohop-darwin-* (run the build step)" >&2; exit 1; }
	scripts/sbom.sh "$VERSION" dist/remohop-linux-amd64 dist/remohop-linux-arm64 \
		dist/remohop-windows-amd64.exe dist/remohop-windows-arm64.exe \
		dist/remohop-darwin-amd64 dist/remohop-darwin-arm64 \
		dist/quick/linux-amd64/remohop-quick dist/quick/linux-arm64/remohop-quick \
		dist/quick/windows-amd64/RemoHop.exe dist/quick/windows-arm64/RemoHop.exe >"$OUT/remohop_sbom.cdx.json"
}

# The universal binary is built, signed and notarized on a macOS runner (see
# packaging/README.md); this only wraps it the same way as the other archives.
step_darwin() {
	local bin="${1:?path to the signed universal remohop}"
	[[ -f "$bin" ]] || { echo "no such file: $bin" >&2; exit 1; }
	mkdir -p "$OUT"; licenses
	local stage; stage="$(mktemp -d)"
	cp "$bin" "$stage/remohop"; chmod 755 "$stage/remohop"
	stage_common "$stage"
	make_tar "$OUT/remohop_darwin_universal.tar.gz" "$stage" remohop LICENSE NOTICE THIRD_PARTY_LICENSES.txt
	rm -rf "$stage"
}

# RemoHop.app is signed, notarized, stapled and zipped (ditto) on a macOS runner;
# the zip is released as it is, because unpacking and packing it again here could
# lose what the signature covers. This checks what Linux can check and copies it.
step_darwin_app() {
	local zip="${1:?path to RemoHop-macos.zip}"
	[[ -f "$zip" ]] || { echo "no such file: $zip" >&2; exit 1; }
	local x; x="$(mktemp -d)"
	unzip -q "$zip" -d "$x"
	local app="$x/RemoHop.app/Contents" f
	for f in Info.plist MacOS/RemoHop _CodeSignature/CodeResources Resources/RemoHop.icns \
		Resources/LICENSE Resources/NOTICE Resources/THIRD_PARTY_LICENSES.txt; do
		[[ -f "$app/$f" ]] || { echo "$zip: RemoHop.app/Contents/$f is missing" >&2; rm -rf "$x"; exit 1; }
	done
	# The stapled notarization ticket (stapler writes Contents/CodeResources).
	[[ -f "$app/CodeResources" ]] || { echo "$zip: no stapled notarization ticket" >&2; rm -rf "$x"; exit 1; }
	[[ "$(head -c 4 "$app/MacOS/RemoHop" | od -An -tx1 | tr -d ' \n')" == cafebabe ]] ||
		{ echo "$zip: RemoHop is not a universal Mach-O" >&2; rm -rf "$x"; exit 1; }
	grep -aqF -- "$VERSION" "$app/MacOS/RemoHop" || { echo "$zip: RemoHop does not carry $VERSION" >&2; rm -rf "$x"; exit 1; }
	grep -qF '<string>com.remohop.quick</string>' "$app/Info.plist" || { echo "$zip: unexpected bundle identifier" >&2; rm -rf "$x"; exit 1; }
	rm -rf "$x"
	mkdir -p "$OUT"
	cp "$zip" "$OUT/RemoHop-macos.zip"
}

case "${1:-}" in
build) step_build ;;
package) step_package ;;
darwin) shift; step_darwin "$@" ;;
darwin-app) shift; step_darwin_app "$@" ;;
all) step_build; step_package ;;
*) sed -n '2,23p' "$0"; exit 2 ;;
esac
