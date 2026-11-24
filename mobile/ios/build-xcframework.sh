#!/usr/bin/env bash
# Builds the Go core for iOS: gomobile bind of mobile/remohopcore (which
# re-exports internal/mobile) into mobile/ios/build/Remohopcore.xcframework,
# the path the Xcode project links. Run on macOS with Xcode before building
# the app; the output is not committed.
#
# The core's version (diagnostics, About) is REMOHOP_CORE_VERSION if set;
# otherwise the app's MARKETING_VERSION plus the short commit (1.0.0+38bab3e),
# or `git describe --tags --always`.
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

core_version="${REMOHOP_CORE_VERSION:-}"
if [[ -z "$core_version" ]]; then
  mv="$(sed -n 's/.*MARKETING_VERSION = \([0-9][0-9.]*\);.*/\1/p' "$here/RemoHop.xcodeproj/project.pbxproj" | head -n 1)"
  sha="$(git -C "$mobile" rev-parse --short HEAD 2>/dev/null || true)"
  if [[ -n "$mv" && -n "$sha" ]]; then core_version="$mv+$sha"
  else core_version="$(git -C "$mobile" describe --tags --always 2>/dev/null || echo dev)"; fi
fi
[[ "$core_version" =~ ^[A-Za-z0-9._+-]{1,64}$ ]] || { echo "build-xcframework.sh: bad core version '$core_version'" >&2; exit 2; }
echo "core version: $core_version"

mkdir -p "$here/build"
rm -rf "$here/build/Remohopcore.xcframework"
(cd "$mobile" && gomobile bind -target=ios,iossimulator -iosversion 16.0 -trimpath \
  -ldflags="-s -w -X github.com/readori/RemoHop/internal/cli.Version=$core_version" \
  -o "$here/build/Remohopcore.xcframework" ./remohopcore)
echo "built $here/build/Remohopcore.xcframework"
