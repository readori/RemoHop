#!/usr/bin/env bash
# Last step before signing: adds the installer scripts to the assets directory and
# writes SHA256SUMS over every file in it. The release workflow then signs
# SHA256SUMS (cosign, keyless) and uploads everything.
#
# Usage: scripts/finalize-release.sh ASSETS_DIR
set -euo pipefail
cd "$(dirname "$0")/.."
dir="${1:?usage: finalize-release.sh ASSETS_DIR}"
[[ -d "$dir" ]] || { echo "no such directory: $dir" >&2; exit 1; }

# The installers ship with each release, so that
# https://github.com/OWNER/REPO/releases/latest/download/install.sh is a stable URL
# and their hashes are covered by the signed SHA256SUMS.
cp packaging/install.sh "$dir/install.sh"
cp packaging/install.ps1 "$dir/install.ps1"

# Every asset a release must have (install.sh, install.ps1, Homebrew, Scoop, winget,
# the AUR and the website link to these names). ALLOW_MISSING is a space-separated
# list of names that may be absent (the release workflow sets it only for SKIP_MACOS).
for must in remohop_linux_amd64.tar.gz remohop_linux_arm64.tar.gz remohop_darwin_universal.tar.gz \
	remohop_windows_amd64.zip remohop_windows_arm64.zip \
	RemoHop-windows-amd64.zip RemoHop-windows-arm64.zip RemoHop-macos.zip RemoHop-macos.dmg \
	RemoHop-QuickSupport-windows-amd64.zip RemoHop-QuickSupport-windows-arm64.zip RemoHop-QuickSupport-macos.zip \
	remohop-app_linux_amd64.tar.gz remohop-app_linux_arm64.tar.gz \
	remohop-quick_linux_amd64.tar.gz remohop-quick_linux_arm64.tar.gz; do
	[[ -f "$dir/$must" ]] || {
		if [[ " ${ALLOW_MISSING:-} " == *" $must "* ]]; then echo "warning: $must is missing (allowed)" >&2
		else echo "missing release asset: $must" >&2; exit 1; fi
	}
done

(
	cd "$dir"
	rm -f SHA256SUMS SHA256SUMS.sigstore.json
	# LC_ALL=C: the same order everywhere.
	find . -maxdepth 1 -type f ! -name '.*' -printf '%f\n' | LC_ALL=C sort | xargs -d '\n' sha256sum -- >.sums.tmp
	mv .sums.tmp SHA256SUMS
	cat SHA256SUMS
)
