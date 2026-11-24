#!/usr/bin/env bash
# Builds the Go core for iOS: gomobile bind of mobile/remohopcore (which
# re-exports internal/mobile) into mobile/ios/build/Remohopcore.xcframework,
# the path the Xcode project links. Run on macOS with Xcode before building
# the app; the output is not committed.
#
# Needs Go (the version in go.mod) and Xcode's command-line tools. gomobile
# and gobind come from the versions pinned in mobile/go.mod.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
mobile="$(cd "$here/.." && pwd)"

tools="$(mktemp -d)"
trap 'rm -rf "$tools"' EXIT
(cd "$mobile" && go build -o "$tools/" golang.org/x/mobile/cmd/gomobile golang.org/x/mobile/cmd/gobind)
export PATH="$tools:$PATH"

mkdir -p "$here/build"
rm -rf "$here/build/Remohopcore.xcframework"
(cd "$mobile" && gomobile bind -target=ios,iossimulator -iosversion 16.0 -trimpath -ldflags='-s -w' \
  -o "$here/build/Remohopcore.xcframework" ./remohopcore)
echo "built $here/build/Remohopcore.xcframework"
