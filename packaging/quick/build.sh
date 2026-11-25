#!/bin/sh
# Builds the double-click packages from any system with Go: the desktop app
# and quick support, which are one program (cmd/remohop-quick) under two
# names (it picks its mode by its file name, see cmd/remohop-quick/launch.go):
#
#   dist/quick/windows-amd64/RemoHop.exe               GUI program (no console), icon + manifest
#   dist/quick/windows-amd64/RemoHop-QuickSupport.exe  the same file, quick support by its name
#   dist/quick/windows-arm64/...                       likewise
#   dist/quick/macos/RemoHop.app                       universal (arm64 + x86_64), UNSIGNED
#   dist/quick/macos/RemoHop Quick Support.app         the same program as RemoHop-QuickSupport
#   dist/quick/linux-amd64/remohop-app                 the desktop app
#   dist/quick/linux-amd64/remohop-quick               the same file, quick support by its name
#   dist/quick/linux-arm64/...                         likewise
#   dist/quick/RemoHop.ico, RemoHop.icns, RemoHop.png
#
# RemoHop.app carries LICENSE, NOTICE and THIRD_PARTY_LICENSES.txt in
# Contents/Resources, so the license texts travel with the signed bundle.
#
# Environment:
#   TARGETS        what to build, any of: windows linux macos icons
#                  (default: all four; the signing workflow builds only macos)
#   VERSION        version string (default: git describe)
#   OUT            output directory (default: dist/quick)
#   EXTRA_LDFLAGS  more linker flags, e.g. the built-in rendezvous server:
#                  -X github.com/readori/RemoHop/internal/dial.DefaultSignal=HOST:21116
#                  Without one, the window asks for the server on first run.
#   GO             go command (default: go)
#
# Signing is not done here; see packaging/quick/README.md.
set -eu

cd "$(dirname "$0")/../.."
GO="${GO:-go}"
VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
OUT="${OUT:-dist/quick}"
LDFLAGS="-s -w -X github.com/readori/RemoHop/internal/cli.Version=$VERSION ${EXTRA_LDFLAGS:-}"
PKG=./cmd/remohop-quick
TARGETS="${TARGETS:-windows linux macos icons}"
export CGO_ENABLED=0

want() { case " $TARGETS " in *" $1 "*) return 0 ;; esac; return 1; }
for t in $TARGETS; do
	case "$t" in windows | linux | macos | icons) ;; *) echo "build.sh: unknown target $t (windows linux macos icons)" >&2; exit 2 ;; esac
done

rm -rf "$OUT"
mkdir -p "$OUT"

if want windows; then
	# The Windows resources (icon, manifest) are linked in from .syso files next
	# to the main package. They are generated, never edited.
	"$GO" run ./packaging/quick/gen -syso cmd/remohop-quick
	for arch in amd64 arm64; do
		echo "building windows/$arch"
		GOOS=windows GOARCH=$arch "$GO" build -trimpath -ldflags "$LDFLAGS -H windowsgui" -o "$OUT/windows-$arch/RemoHop.exe" $PKG
		"$GO" run ./packaging/quick/gen -verify "$OUT/windows-$arch/RemoHop.exe"
		cp "$OUT/windows-$arch/RemoHop.exe" "$OUT/windows-$arch/RemoHop-QuickSupport.exe"
	done
fi

if want linux; then
	for arch in amd64 arm64; do
		echo "building linux/$arch"
		GOOS=linux GOARCH=$arch "$GO" build -trimpath -ldflags "$LDFLAGS" -o "$OUT/linux-$arch/remohop-app" $PKG
		cp "$OUT/linux-$arch/remohop-app" "$OUT/linux-$arch/remohop-quick"
	done
fi

if want macos; then
	tmp="$(mktemp -d)"
	trap 'rm -rf "$tmp"' EXIT
	for arch in amd64 arm64; do
		echo "building darwin/$arch"
		GOOS=darwin GOARCH=$arch "$GO" build -trimpath -ldflags "$LDFLAGS" -o "$tmp/remohop-quick-$arch" $PKG
	done
	bash packaging/third_party_licenses.sh >"$tmp/THIRD_PARTY_LICENSES.txt"
	for kind in app quick; do
		app="$OUT/macos/RemoHop.app" flag=
		if [ "$kind" = quick ]; then app="$OUT/macos/RemoHop Quick Support.app" flag=-quick-support; fi
		"$GO" run ./packaging/quick/gen -app "$app" $flag -version "$VERSION" \
			-binary "arm64=$tmp/remohop-quick-arm64,amd64=$tmp/remohop-quick-amd64"
		# The licenses go inside the bundle, before it is signed (the signature
		# seals Contents/Resources), so a downloaded app carries them.
		cp "$tmp/THIRD_PARTY_LICENSES.txt" LICENSE NOTICE "$app/Contents/Resources/"
	done
fi

if want icons; then
	"$GO" run ./packaging/quick/gen -ico "$OUT/RemoHop.ico" -icns "$OUT/RemoHop.icns" -png "$OUT/RemoHop.png"
fi

echo "done: $OUT ($TARGETS; version $VERSION; nothing is signed)"
