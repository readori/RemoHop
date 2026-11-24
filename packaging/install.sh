#!/bin/sh
# Install the remohop binary from a RemoHop GitHub release (Linux and macOS).
#
#   curl -fsSLO https://github.com/readori/RemoHop/releases/latest/download/install.sh
#   less install.sh        # read it first; it is short
#   sh install.sh
#
# What it does: downloads remohop_<os>_<arch>.tar.gz and SHA256SUMS from the release,
# checks the archive against SHA256SUMS, checks SHA256SUMS' signature when cosign is
# installed, and copies `remohop` into a directory. It never runs sudo and never edits
# your shell profile. As a normal user it installs to ~/.local/bin; as root, to
# /usr/local/bin. For a system-wide install without running this as root:
#   BINDIR=/usr/local/bin sh install.sh     # fails if you cannot write there
#
# Options (or the environment variable in brackets):
#   --version vX.Y.Z   release to install               [VERSION]  default: latest
#   --bindir DIR       install directory                [BINDIR]
#   --repo OWNER/NAME  GitHub repository                [REPO]     default: readori/RemoHop
#   --verify MODE      signature check: auto, require, off   [VERIFY]  default: auto
#                      auto: verify with cosign if it is installed, else say so and
#                      continue on the checksum alone; require: fail without a valid
#                      signature; off: skip it
#   --base-url URL     directory with the assets (a mirror or a test server)   [BASE_URL]
#   -h, --help
#
# Trust: the checksum alone only detects a damaged download, because SHA256SUMS comes
# from the same place as the archive. The signature is what ties SHA256SUMS to the
# release workflow of this repository (cosign keyless, see packaging/README.md);
# install cosign (https://docs.sigstore.dev/cosign/system_config/installation/) and use
# --verify require to get that guarantee.
set -eu

REPO="${REPO:-readori/RemoHop}"
VERSION="${VERSION:-}"
BINDIR="${BINDIR:-}"
VERIFY="${VERIFY:-auto}"
BASE_URL="${BASE_URL:-}"

say() { printf '%s\n' "$*"; }
warn() { printf 'install.sh: %s\n' "$*" >&2; }
die() { warn "$*"; exit 1; }
usage() { sed -n '2,/^set -eu/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'; }

while [ $# -gt 0 ]; do
	case "$1" in
	--version) [ $# -ge 2 ] || die "--version needs a value"; VERSION="$2"; shift 2 ;;
	--bindir) [ $# -ge 2 ] || die "--bindir needs a value"; BINDIR="$2"; shift 2 ;;
	--repo) [ $# -ge 2 ] || die "--repo needs a value"; REPO="$2"; shift 2 ;;
	--verify) [ $# -ge 2 ] || die "--verify needs a value"; VERIFY="$2"; shift 2 ;;
	--base-url) [ $# -ge 2 ] || die "--base-url needs a value"; BASE_URL="$2"; shift 2 ;;
	-h | --help) usage; exit 0 ;;
	*) die "unknown option $1 (try --help)" ;;
	esac
done
case "$VERIFY" in auto | require | off) ;; *) die "--verify must be auto, require or off" ;; esac
case "$REPO" in */*) ;; *) die "REPO must be OWNER/NAME" ;; esac

case "$(uname -s)" in
Linux) os=linux ;;
Darwin) os=darwin ;;
*) die "unsupported OS $(uname -s). On Windows use install.ps1, or download the zip from https://github.com/$REPO/releases" ;;
esac
case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*) die "unsupported architecture $(uname -m)" ;;
esac
# macOS ships one universal (arm64 + x86_64) signed and notarized binary.
if [ "$os" = darwin ]; then arch=universal; fi
asset="remohop_${os}_${arch}.tar.gz"

if [ -n "$BASE_URL" ]; then
	base="${BASE_URL%/}"
elif [ -n "$VERSION" ]; then
	case "$VERSION" in v[0-9]*) ;; [0-9]*) VERSION="v$VERSION" ;; *) die "VERSION must look like v1.2.3" ;; esac
	base="https://github.com/$REPO/releases/download/$VERSION"
else
	base="https://github.com/$REPO/releases/latest/download"
fi

if [ -n "$BINDIR" ]; then
	dest="$BINDIR"
elif [ "$(id -u)" -eq 0 ]; then
	dest=/usr/local/bin
else
	dest="${HOME:?HOME is not set; use --bindir}/.local/bin"
fi

if command -v curl >/dev/null 2>&1; then
	fetch() { curl -fsSL --retry 3 -o "$2" "$1"; }
elif command -v wget >/dev/null 2>&1; then
	fetch() { wget -q -O "$2" "$1"; }
else
	die "need curl or wget"
fi
if command -v sha256sum >/dev/null 2>&1; then
	sha256() { sha256sum "$1" | cut -d ' ' -f 1; }
elif command -v shasum >/dev/null 2>&1; then
	sha256() { shasum -a 256 "$1" | cut -d ' ' -f 1; }
else
	die "need sha256sum or shasum to verify the download"
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

say "Downloading $asset from $base"
fetch "$base/$asset" "$tmp/$asset" || die "download failed: $base/$asset"
fetch "$base/SHA256SUMS" "$tmp/SHA256SUMS" || die "download failed: $base/SHA256SUMS"

# 1. The signature on SHA256SUMS (who made this list).
sig_ok=no
if [ "$VERIFY" != off ]; then
	if command -v cosign >/dev/null 2>&1; then
		fetch "$base/SHA256SUMS.sigstore.json" "$tmp/SHA256SUMS.sigstore.json" ||
			die "download failed: $base/SHA256SUMS.sigstore.json (use --verify off to skip the signature check)"
		# The certificate must name this repository's release workflow, run for a tag.
		if cosign verify-blob \
			--bundle "$tmp/SHA256SUMS.sigstore.json" \
			--certificate-identity-regexp "^https://github\\.com/$(printf '%s' "$REPO" | sed 's/[.]/\\./g')/\\.github/workflows/release\\.yml@refs/tags/v.+\$" \
			--certificate-oidc-issuer https://token.actions.githubusercontent.com \
			"$tmp/SHA256SUMS" >"$tmp/cosign.out" 2>&1; then
			sig_ok=yes
			say "Signature OK (cosign: $REPO release workflow)"
		else
			cat "$tmp/cosign.out" >&2
			die "the signature of SHA256SUMS is not valid; nothing was installed"
		fi
	elif [ "$VERIFY" = require ]; then
		die "--verify require, but cosign is not installed"
	else
		warn "cosign is not installed: the signature was NOT checked (the checksum below only detects a damaged download). See --help."
	fi
fi

# 2. The archive against SHA256SUMS (is this the file that was listed).
want="$(awk -v f="$asset" '$2 == f || $2 == "*" f { print $1; exit }' "$tmp/SHA256SUMS")"
[ -n "$want" ] || die "$asset is not listed in SHA256SUMS"
got="$(sha256 "$tmp/$asset")"
[ "$got" = "$want" ] || die "checksum mismatch for $asset (expected $want, got $got); nothing was installed"
say "Checksum OK"

tar -xzf "$tmp/$asset" -C "$tmp" remohop || die "could not extract remohop from $asset"
[ -f "$tmp/remohop" ] || die "the archive has no remohop binary"

if ! mkdir -p "$dest" 2>/dev/null || [ ! -w "$dest" ]; then
	die "cannot write to $dest. Pick a directory you own (--bindir ~/bin), or run this as root for a system-wide install. Nothing was installed."
fi
# Copy next to the target and rename, so a running remohop is replaced atomically.
cp "$tmp/remohop" "$dest/.remohop.new.$$"
chmod 755 "$dest/.remohop.new.$$"
mv -f "$dest/.remohop.new.$$" "$dest/remohop"

say "Installed $dest/remohop"
"$dest/remohop" version || true
if [ "$sig_ok" = yes ]; then say "Verified: checksum and signature."; else say "Verified: checksum only."; fi
case ":$PATH:" in
*":$dest:"*) ;;
*) say "Note: $dest is not on your PATH. Add it, for example:  export PATH=\"$dest:\$PATH\"" ;;
esac
say "Next: remohop host   (or: remohop service install  to run it at boot/login)"
