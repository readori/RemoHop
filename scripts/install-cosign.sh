#!/usr/bin/env bash
# Installs a pinned cosign into DIR (default: ./.tools) after checking its SHA-256.
# The hashes below are from the project's own cosign_checksums.txt for this release
# (https://github.com/sigstore/cosign/releases). To upgrade: change COSIGN_VERSION and
# both hashes together, from that file, and read the diff.
#
# Usage: scripts/install-cosign.sh [DIR]    # prints the path of the cosign binary
set -euo pipefail
COSIGN_VERSION=v2.6.1
# shellcheck disable=SC2034  # read through indirection below
SHA_linux_amd64=064954c5d8c7e3b28188eee5b1727b31c411550bc5fefd41aa672d3c761d103a
# shellcheck disable=SC2034
SHA_linux_arm64=56a16480bdd56ec789abaa65924402f6b92c0041f06885995853c05567b76f34

dir="${1:-.tools}"
case "$(uname -m)" in x86_64) arch=amd64 ;; aarch64 | arm64) arch=arm64 ;; *) echo "unsupported arch" >&2; exit 1 ;; esac
[[ "$(uname -s)" == Linux ]] || { echo "install-cosign.sh supports Linux runners only" >&2; exit 1; }
want_var="SHA_linux_$arch"; want="${!want_var}"
mkdir -p "$dir"
tmp="$(mktemp)"; trap 'rm -f "$tmp"' EXIT
curl -fsSL --retry 3 -o "$tmp" "https://github.com/sigstore/cosign/releases/download/$COSIGN_VERSION/cosign-linux-$arch"
got="$(sha256sum "$tmp" | cut -d' ' -f1)"
[[ "$got" == "$want" ]] || { echo "cosign checksum mismatch: got $got, want $want" >&2; exit 1; }
install -m 755 "$tmp" "$dir/cosign"
echo "$dir/cosign"
