#!/usr/bin/env bash
# Points packaging/snap/snapcraft.yaml at another release: downloads the
# release's SHA256SUMS and its cosign bundle, verifies the signature (the
# release workflow's keyless identity for exactly that tag, as install.sh
# does, so another release's files cannot be pinned), checks that both
# Linux archives are listed, and rewrites VERSION and SUMS_SHA256 in the pull
# step. The snap build then trusts nothing but that pinned hash.
#
# Usage: packaging/snap/bump.sh vX.Y.Z [--verify auto|require|skip]
#   auto     verify with cosign when it is installed, else warn (default)
#   require  fail unless cosign is installed and the signature checks out
#   skip     do not verify (offline tests only)
# Environment: REPO (default readori/RemoHop), COSIGN (default: cosign on PATH).
set -euo pipefail

usage() { sed -n '2,13p' "$0" | sed 's/^# \{0,1\}//' >&2; exit 2; }
version=${1:-}
[[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]] || usage
shift
verify=auto
while [ $# -gt 0 ]; do
	case "$1" in
	--verify) verify=${2:-}; shift 2 || usage ;;
	*) usage ;;
	esac
done
case "$verify" in auto | require | skip) ;; *) usage ;; esac

REPO=${REPO:-readori/RemoHop}
COSIGN=${COSIGN:-cosign}
yaml="$(cd "$(dirname "$0")" && pwd)/snapcraft.yaml"
base="https://github.com/$REPO/releases/download/$version"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fetch() { curl -fsSL --retry 3 -o "$tmp/$1" "$base/$1" || { echo "bump.sh: cannot download $base/$1" >&2; exit 1; }; }
fetch SHA256SUMS
fetch SHA256SUMS.sigstore.json

if [ "$verify" != skip ] && command -v "$COSIGN" >/dev/null 2>&1; then
	"$COSIGN" verify-blob --bundle "$tmp/SHA256SUMS.sigstore.json" \
		--certificate-identity "https://github.com/$REPO/.github/workflows/release.yml@refs/tags/$version" \
		--certificate-oidc-issuer https://token.actions.githubusercontent.com \
		"$tmp/SHA256SUMS" >&2
	echo "bump.sh: signature of SHA256SUMS OK ($REPO release workflow, $version)" >&2
elif [ "$verify" = require ]; then
	echo "bump.sh: --verify require, but $COSIGN is not installed (scripts/install-cosign.sh)" >&2
	exit 1
elif [ "$verify" = auto ]; then
	echo "bump.sh: WARNING: cosign is not installed, the signature of SHA256SUMS was NOT checked" >&2
fi

for arch in amd64 arm64; do
	n=$(grep -cE "^[0-9a-f]{64}  remohop_linux_$arch\\.tar\\.gz\$" "$tmp/SHA256SUMS" || true)
	[ "$n" = 1 ] || { echo "bump.sh: remohop_linux_$arch.tar.gz is not listed once in SHA256SUMS of $version" >&2; exit 1; }
done

sum=$(sha256sum "$tmp/SHA256SUMS" | cut -d' ' -f1)
if ! grep -qE '^      VERSION=v' "$yaml" || ! grep -qE '^      SUMS_SHA256=[0-9a-f]{64}$' "$yaml"; then
	echo "bump.sh: VERSION= or SUMS_SHA256= line not found in $yaml" >&2
	exit 1
fi
sed -i -E \
	-e "s/^(      VERSION=)v.*/\\1$version/" \
	-e "s/^(      SUMS_SHA256=)[0-9a-f]{64}$/\\1$sum/" "$yaml"
echo "bump.sh: $yaml now packages $version (SHA256SUMS $sum)" >&2
