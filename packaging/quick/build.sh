#!/bin/sh
# Builds the double-click ("quick support") packages from any system with Go:
#
#   dist/quick/windows-amd64/RemoHop.exe   GUI program (no console), icon + manifest
#   dist/quick/windows-arm64/RemoHop.exe
#   dist/quick/macos/RemoHop.app           universal (arm64 + x86_64), UNSIGNED
#   dist/quick/linux-amd64/remohop-quick
#   dist/quick/linux-arm64/remohop-quick
#
# Environment:
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
export CGO_ENABLED=0

rm -rf "$OUT"
mkdir -p "$OUT"

# The Windows resources (icon, manifest) are linked in from .syso files next
# to the main package. They are generated, never edited.
"$GO" run ./packaging/quick/gen -syso cmd/remohop-quick

for arch in amd64 arm64; do
	echo "building windows/$arch"
	GOOS=windows GOARCH=$arch "$GO" build -trimpath -ldflags "$LDFLAGS -H windowsgui" -o "$OUT/windows-$arch/RemoHop.exe" $PKG
	"$GO" run ./packaging/quick/gen -verify "$OUT/windows-$arch/RemoHop.exe"
done

for arch in amd64 arm64; do
	echo "building linux/$arch"
	GOOS=linux GOARCH=$arch "$GO" build -trimpath -ldflags "$LDFLAGS" -o "$OUT/linux-$arch/remohop-quick" $PKG
done

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
for arch in amd64 arm64; do
	echo "building darwin/$arch"
	GOOS=darwin GOARCH=$arch "$GO" build -trimpath -ldflags "$LDFLAGS" -o "$tmp/remohop-quick-$arch" $PKG
done
"$GO" run ./packaging/quick/gen -app "$OUT/macos/RemoHop.app" -version "$VERSION" \
	-binary "arm64=$tmp/remohop-quick-arm64,amd64=$tmp/remohop-quick-amd64"
"$GO" run ./packaging/quick/gen -ico "$OUT/RemoHop.ico" -icns "$OUT/RemoHop.icns" -png "$OUT/RemoHop.png"

echo "done: $OUT (version $VERSION; nothing is signed)"
