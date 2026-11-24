#!/usr/bin/env bash
# Builds the RemoHop Android app: gomobile bind (the Go core, internal/mobile
# through mobile/remohopcore) into app/libs/remohopcore.aar, then Gradle.
#
#   mobile/android/build.sh                 debug APK + JVM unit tests
#   mobile/android/build.sh assembleRelease  any Gradle tasks instead
#
# Needs: Go (the version in go.mod), JDK 17+, ANDROID_HOME with
# platforms;android-35, build-tools;35.0.0 and an NDK (ANDROID_NDK_HOME, or
# the newest under $ANDROID_HOME/ndk). gomobile and gobind are built from
# the versions pinned in mobile/go.mod; nothing is installed globally.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
mobile="$(cd "$here/.." && pwd)"

: "${ANDROID_HOME:=${ANDROID_SDK_ROOT:-}}"
[[ -n "$ANDROID_HOME" && -d "$ANDROID_HOME" ]] || { echo "build.sh: set ANDROID_HOME to the Android SDK" >&2; exit 2; }
export ANDROID_HOME
if [[ -z "${ANDROID_NDK_HOME:-}" ]]; then
  ndk="$(ls -d "$ANDROID_HOME"/ndk/* 2>/dev/null | sort -V | tail -1 || true)"
  [[ -n "$ndk" ]] || { echo "build.sh: no NDK under $ANDROID_HOME/ndk; install one with sdkmanager" >&2; exit 2; }
  export ANDROID_NDK_HOME="$ndk"
fi

tools="$(mktemp -d)"
trap 'rm -rf "$tools"' EXIT
(cd "$mobile" && go build -o "$tools/" golang.org/x/mobile/cmd/gomobile golang.org/x/mobile/cmd/gobind)
export PATH="$tools:$PATH"

mkdir -p "$here/app/libs"
(cd "$mobile" && gomobile bind -target=android/arm64,android/amd64 -androidapi 26 \
  -javapkg com.remohop.app.core -trimpath -ldflags='-s -w' \
  -o "$here/app/libs/remohopcore.aar" ./remohopcore)
rm -f "$here/app/libs/remohopcore-sources.jar"

cd "$here"
if [[ $# -eq 0 ]]; then
  set -- assembleDebug testDebugUnitTest
fi
./gradlew --no-daemon "$@"
