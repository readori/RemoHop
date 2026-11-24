#!/usr/bin/env bash
# Builds dummy_hcd.ko (the kernel's virtual USB host + device controller
# pair, drivers/usb/gadget/udc/dummy_hcd.c) for the running kernel, for
# kernels whose distribution does not ship it (Ubuntu's azure kernels on
# GitHub runners have no linux-modules-extra, and dummy_hcd is not in the
# base modules). It is a test fixture for test/e2e/usbip.sh, not part of remohop.
#
# The source file comes from kernel.org's stable tree at the tag of this
# kernel's upstream version (from /proc/version_signature on Ubuntu, else
# MAJOR.MINOR); its SHA-256 is printed. It needs the kernel headers
# (installed with apt if missing) and the compiler the kernel was built with.
#
# Usage: test/e2e/build_dummy_hcd.sh OUTDIR    (prints the module's path last)
set -euo pipefail
OUT=${1:?usage: build_dummy_hcd.sh OUTDIR}
K=$(uname -r)
mkdir -p "$OUT"
cd "$OUT"

as_root() { if [ "$(id -u)" = 0 ]; then "$@"; else sudo "$@"; fi; }

if [ ! -e "/lib/modules/$K/build/Makefile" ]; then
	echo "installing linux-headers-$K" >&2
	as_root env DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "linux-headers-$K" >/dev/null
fi

# The compiler the kernel was built with ("gcc-15 (Ubuntu ...)" in /proc/version).
CC=gcc
v=$(grep -oE 'gcc-[0-9]+' /proc/version | head -n 1 || true)
if [ -n "$v" ]; then
	command -v "$v" >/dev/null || as_root env DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "$v" >/dev/null || true
	if command -v "$v" >/dev/null; then CC=$v; fi
fi

# Upstream version: Ubuntu writes it last in /proc/version_signature
# ("Ubuntu 7.0.0-1020.21-azure 7.0.3"); otherwise MAJOR.MINOR of uname -r.
up=$(awk '{print $NF}' /proc/version_signature 2>/dev/null || true)
[[ "$up" =~ ^[0-9]+\.[0-9]+(\.[0-9]+)?$ ]] || up=$(echo "$K" | grep -oE '^[0-9]+\.[0-9]+')
mm=$(echo "$up" | grep -oE '^[0-9]+\.[0-9]+')
got=
for tag in "v$up" "v$mm"; do
	url="https://git.kernel.org/pub/scm/linux/kernel/git/stable/linux.git/plain/drivers/usb/gadget/udc/dummy_hcd.c?h=$tag"
	if curl -fsSL --retry 3 --max-time 60 -o dummy_hcd.c "$url" && grep -q 'dummy_hcd' dummy_hcd.c; then got=$tag; break; fi
done
[ -n "$got" ] || { echo "build_dummy_hcd: could not fetch dummy_hcd.c for $up" >&2; exit 1; }
echo "dummy_hcd.c from linux $got (kernel.org stable), sha256 $(sha256sum dummy_hcd.c | cut -d' ' -f1)" >&2

echo 'obj-m := dummy_hcd.o' >Makefile
if ! make -s -C "/lib/modules/$K/build" M="$PWD" CC="$CC" modules >build.log 2>&1; then
	echo "build_dummy_hcd: build failed (CC=$CC):" >&2
	tail -n 30 build.log >&2
	exit 1
fi
echo "built dummy_hcd.ko for $K with $CC" >&2
echo "$PWD/dummy_hcd.ko"
