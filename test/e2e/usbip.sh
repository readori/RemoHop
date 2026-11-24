#!/usr/bin/env bash
# End-to-end test of `remohop <target> usb` against the real Linux USB/IP
# drivers: a virtual USB mass-storage device (a configfs gadget on dummy_hcd,
# backed by a file of random bytes) is exported by a remohop host through
# usbip-host and imported by a remohop client through vhci_hcd, on the same
# machine over loopback. The data read through the imported disk must be
# the backing file's, a write through it must land in the file, and detach
# and session end must give the device back to its own driver.
#
# Needs root and the kernel modules usbip-core, usbip-host, vhci-hcd,
# dummy_hcd, libcomposite and usb_f_mass_storage (Ubuntu:
# linux-modules-extra-$(uname -r)). Where dummy_hcd is not shipped,
# DUMMY_HCD_KO may name one built by test/e2e/build_dummy_hcd.sh. Everything
# it creates is removed on exit (a loaded module stays loaded).
#
# Usage: sudo [DUMMY_HCD_KO=/path/dummy_hcd.ko] test/e2e/usbip.sh /path/to/remohop
set -uo pipefail
. "$(dirname "$0")/common.sh"

REMOHOP=$(abspath "${1:?usage: usbip.sh /path/to/remohop}")
WORK=$(mktemp -d /tmp/remohop-usb.XXXXXX)
GADGET=/sys/kernel/config/usb_gadget/remohop_e2e
VID=1d6b PID=0104 SERIAL=REMOHOPE2E0001
SIZE_MB=16
PORT=$((20000 + RANDOM % 9000))
PASSWORD=usb-e2e-$RANDOM-$RANDOM
HOST_PID=
BUSID=
VHCI=/sys/devices/platform/vhci_hcd.0

host() { REMOHOP_CONFIG_DIR=$WORK/host "$REMOHOP" "$@"; }
client() { REMOHOP_CONFIG_DIR=$WORK/cli "$REMOHOP" "$@"; }

gadget_down() {
	[ -d "$GADGET" ] || return 0
	echo "" >"$GADGET/UDC" 2>/dev/null
	rm -f "$GADGET/configs/c.1/mass_storage.0"
	rmdir "$GADGET/configs/c.1/strings/0x409" "$GADGET/configs/c.1" \
		"$GADGET/functions/mass_storage.0" "$GADGET/strings/0x409" "$GADGET" 2>/dev/null
}

cleanup() {
	client stop --all >/dev/null 2>&1
	# Detach whatever is still imported through vhci (this test is the only
	# vhci user on a CI runner).
	if [ -r "$VHCI/status" ]; then
		awk '$3 == "006" {print $2+0}' "$VHCI"/status* 2>/dev/null | while read -r p; do echo "$p" >"$VHCI/detach" 2>/dev/null; done
	fi
	[ -z "$HOST_PID" ] || { kill "$HOST_PID" 2>/dev/null; sleep 1; }
	if [ -n "$BUSID" ] && [ "$(basename "$(readlink "/sys/bus/usb/devices/$BUSID/driver" 2>/dev/null)")" = usbip-host ]; then
		host usb release "$BUSID" >/dev/null 2>&1
	fi
	gadget_down
	rm -rf "$WORK"
}
trap cleanup EXIT

[ "$(id -u)" = 0 ] || { echo "usbip: must run as root" >&2; exit 2; }

# blk.py read PATH LEN [OFFSET] -> sha256 of LEN bytes at OFFSET
#        write PATH OFFSET FILE  -> writes FILE (a multiple of 4096 bytes)
# Block devices are opened with O_DIRECT (and O_SYNC for writes) through a
# page-aligned buffer.
cat >"$WORK/blk.py" <<'PY'
import hashlib, mmap, os, stat, sys
op, path = sys.argv[1], sys.argv[2]
blk = stat.S_ISBLK(os.stat(path).st_mode)
CH = 1 << 20
buf = mmap.mmap(-1, CH)
if op == "read":
    size = int(sys.argv[3]); off = int(sys.argv[4]) if len(sys.argv) > 4 else 0
    fd = os.open(path, os.O_RDONLY | (os.O_DIRECT if blk else 0))
    h = hashlib.sha256(); left = size
    while left > 0:
        n = os.preadv(fd, [buf], off)
        if n <= 0:
            sys.exit("short read at %d" % off)
        take = min(n, left)
        h.update(buf[:take]); left -= take; off += take
    print(h.hexdigest())
else:
    off = int(sys.argv[3]); data = open(sys.argv[4], "rb").read()
    fd = os.open(path, os.O_WRONLY | ((os.O_DIRECT | os.O_SYNC) if blk else 0))
    for i in range(0, len(data), CH):
        part = data[i:i + CH]
        buf.seek(0); buf.write(part)
        if os.pwritev(fd, [memoryview(buf)[:len(part)]], off + i) != len(part):
            sys.exit("short write")
    os.fsync(fd)
PY

drv() { basename "$(readlink "/sys/bus/usb/devices/$1/driver" 2>/dev/null)" 2>/dev/null; }
attr() { cat "/sys/bus/usb/devices/$1/$2" 2>/dev/null; }

# blockdev USB-BUSID: the disk the kernel made of that device, if any.
blockdev() {
	local b
	for b in /sys/bus/usb/devices/"$1"/"$1":*/host*/target*/*/block/*; do
		[ -e "$b" ] && { basename "$b"; return 0; }
	done
	return 1
}

say "$("$REMOHOP" version) on $(uname -sr)"

say "kernel"
# libcomposite brings udc-core, which dummy_hcd needs: load it first.
for m in usbip-core usbip-host vhci-hcd libcomposite usb_f_mass_storage dummy_hcd; do
	if modprobe "$m" 2>"$WORK/mod.txt"; then
		ok "modprobe $m"
	elif [ "$m" = dummy_hcd ] && [ -n "${DUMMY_HCD_KO:-}" ] && [ -f "$DUMMY_HCD_KO" ]; then
		# The distribution does not ship it: a module built from the
		# kernel's own source by test/e2e/build_dummy_hcd.sh.
		if lsmod 2>/dev/null | grep -q '^dummy_hcd ' || insmod "$DUMMY_HCD_KO" 2>"$WORK/mod.txt"; then
			ok "insmod dummy_hcd (built from upstream source: not shipped for $(uname -r))"
		else
			fail "insmod $DUMMY_HCD_KO: $(head -c 300 "$WORK/mod.txt") (lockdown: $(cat /sys/kernel/security/lockdown 2>/dev/null || echo unknown))"
		fi
	else
		fail "modprobe $m: $(head -c 300 "$WORK/mod.txt")"
	fi
done
[ "$FAILED" = 0 ] || { echo "usbip: kernel modules missing; nothing else can run"; finish usbip; }
mountpoint -q /sys/kernel/config || mount -t configfs none /sys/kernel/config
UDC=$(ls /sys/class/udc 2>/dev/null | grep -m1 '^dummy_udc') || true
if [ -n "$UDC" ]; then ok "UDC $UDC"; else fail "no dummy_udc in /sys/class/udc"; finish usbip; fi

say "virtual USB disk (configfs mass-storage gadget on dummy_hcd)"
head -c $((SIZE_MB << 20)) /dev/urandom >"$WORK/disk.img"
IMG_SUM=$(sha256sum <"$WORK/disk.img" | cut -d' ' -f1)
gadget_down
mkdir -p "$GADGET/strings/0x409" "$GADGET/configs/c.1/strings/0x409" "$GADGET/functions/mass_storage.0"
echo "0x$VID" >"$GADGET/idVendor"
echo "0x$PID" >"$GADGET/idProduct"
echo "$SERIAL" >"$GADGET/strings/0x409/serialnumber"
echo "RemoHop" >"$GADGET/strings/0x409/manufacturer"
echo "e2e disk" >"$GADGET/strings/0x409/product"
echo "mass storage" >"$GADGET/configs/c.1/strings/0x409/configuration"
echo 0 >"$GADGET/functions/mass_storage.0/lun.0/removable"
echo "$WORK/disk.img" >"$GADGET/functions/mass_storage.0/lun.0/file"
ln -s "$GADGET/functions/mass_storage.0" "$GADGET/configs/c.1/"
echo "$UDC" >"$GADGET/UDC"

find_gadget() {
	local d
	for d in /sys/bus/usb/devices/*; do
		[ "$(cat "$d/idVendor" 2>/dev/null)" = $VID ] && [ "$(cat "$d/idProduct" 2>/dev/null)" = $PID ] &&
			[ "$(cat "$d/serial" 2>/dev/null)" = $SERIAL ] && ! readlink -f "$d" | grep -q vhci_hcd && { basename "$d"; return 0; }
	done
	return 1
}
if wait_until 15 find_gadget; then
	BUSID=$(find_gadget)
	ok "gadget enumerated on the host side as $BUSID (driver $(drv "$BUSID"), speed $(attr "$BUSID" speed) Mbit/s)"
else
	fail "the gadget did not enumerate"; dmesg | tail -n 20 | sed 's/^/         /'; finish usbip
fi
if wait_until 15 "blockdev $BUSID"; then info "host-side disk before export: /dev/$(blockdev "$BUSID")"; else info "no host-side disk appeared for $BUSID (usb-storage did not bind)"; fi

say "remohop host and client on loopback"
mkdir -p "$WORK/host" "$WORK/cli"
REMOHOP_CONFIG_DIR=$WORK/host "$REMOHOP" host --no-signal --listen "127.0.0.1:$PORT" --password "$PASSWORD" >"$WORK/host.log" 2>&1 </dev/null &
HOST_PID=$!
if wait_until 15 "grep -q 'host ready' '$WORK/host.log'"; then ok "host started (as root)"; else fail "host did not start"; tail_of "$WORK/host.log"; finish usbip; fi
check "pair" env REMOHOP_CONFIG_DIR="$WORK/cli" REMOHOP_PASSWORD="$PASSWORD" "$REMOHOP" "127.0.0.1:$PORT" pair --name usbhost

code=0; host usb allow "$BUSID" >"$WORK/out.txt" 2>&1 || code=$?
if [ "$code" != 0 ] && grep -q 'storage' "$WORK/out.txt"; then ok "usb allow refuses a storage device without --force"; else fail "allow without --force (exit $code)"; tail_of "$WORK/out.txt"; fi
check "remohop usb allow --force $BUSID (on the host)" host usb allow --force "$BUSID"
if host usb allowed | grep -q "^$BUSID .*$VID:$PID"; then ok "remohop usb allowed lists $BUSID $VID:$PID"; else fail "usb allowed"; fi

client usbhost usb list --json >"$WORK/list.json" 2>"$WORK/list.err"
if python3 -c 'import json,sys; d=[x for x in json.load(open(sys.argv[1])) if x["busid"]==sys.argv[2]]; sys.exit(0 if d and d[0]["allowed"] and d[0]["vid"]==sys.argv[3] and "mass-storage" in d[0].get("interfaces",[]) else 1)' "$WORK/list.json" "$BUSID" $VID; then
	ok "remohop usbhost usb list: $BUSID, allowed, mass-storage"
else
	fail "usb list"; tail_of "$WORK/list.json"; tail_of "$WORK/list.err"
fi

# attach_and_find: attach in the background and wait for the vhci port.
attach_and_find() {
	if ! client usbhost usb attach -b "$BUSID" >"$WORK/attach.txt" 2>&1; then return 1; fi
	wait_until 20 "client usb --json | python3 -c 'import json,sys; sys.exit(0 if any(a.get(\"busid\")==\"$BUSID\" for a in json.load(sys.stdin)) else 1)'" || return 1
	read -r VPORT LBUSID < <(client usb --json | python3 -c 'import json,sys
for a in json.load(sys.stdin):
    if a.get("busid")==sys.argv[1]: print(a["port"], a["local_busid"])' "$BUSID")
	[ -n "${LBUSID:-}" ]
}

say "attach"
if attach_and_find; then
	ok "remohop usbhost usb attach -b $BUSID: vhci port $VPORT, local bus id $LBUSID"
	sed 's/^/         /' "$WORK/attach.txt"
else
	fail "attach"; tail_of "$WORK/attach.txt"; tail_of "$WORK/host.log"; dmesg | tail -n 20 | sed 's/^/         /'; finish usbip
fi
if awk -v p="$VPORT" -v b="$LBUSID" '$2+0 == p && $3 == "006" && $7 == b {f=1} END {exit !f}' "$VHCI"/status*; then
	ok "vhci_hcd status: port $VPORT in use by $LBUSID"
else
	fail "vhci status does not show port $VPORT"; sed 's/^/         /' "$VHCI/status"
fi
if wait_until 15 "[ \"\$(attr $LBUSID idVendor)\" = $VID ]"; then
	ok "imported device $LBUSID: $(attr "$LBUSID" idVendor):$(attr "$LBUSID" idProduct) serial $(attr "$LBUSID" serial), under $(readlink -f "/sys/bus/usb/devices/$LBUSID" | grep -o 'vhci_hcd\.[0-9]*')"
else
	fail "no device $LBUSID with $VID:$PID in sysfs"
fi
if command -v lsusb >/dev/null; then
	bus=${LBUSID%%-*}
	if lsusb -s "$bus:" -d "$VID:$PID" | grep -q .; then ok "lsusb: $(lsusb -s "$bus:" -d "$VID:$PID" | head -1)"; else fail "lsusb does not show $VID:$PID on bus $bus"; fi
fi
if [ "$(drv "$BUSID")" = usbip-host ] && [ "$(attr "$BUSID" usbip_status)" = 2 ]; then
	ok "host side: $BUSID bound to usbip-host, usbip_status 2 (in use)"
else
	fail "host side: driver '$(drv "$BUSID")' usbip_status '$(attr "$BUSID" usbip_status)'"
fi
client usbhost usb list --json >"$WORK/list.json" 2>/dev/null
if python3 -c 'import json,sys; d=[x for x in json.load(open(sys.argv[1])) if x["busid"]==sys.argv[2]]; sys.exit(0 if d and d[0]["exported"] and d[0]["in_use"] else 1)' "$WORK/list.json" "$BUSID"; then
	ok "usb list shows $BUSID exported and in use"
else
	fail "usb list during attach"; tail_of "$WORK/list.json"
fi

say "data through the imported disk"
if wait_until 20 "blockdev $LBUSID"; then
	DEV=/dev/$(blockdev "$LBUSID")
	wait_until 10 "[ -b $DEV ]"
	size=$(($(cat "/sys/class/block/${DEV#/dev/}/size") * 512))
	if [ "$size" = $((SIZE_MB << 20)) ]; then ok "$DEV is the imported disk, $SIZE_MB MiB"; else fail "$DEV size $size"; fi
	# Direct I/O with an aligned buffer, so the bytes come from the device
	# through USB/IP, not from the page cache. (Python rather than dd:
	# Ubuntu's dd is now uutils, whose iflag=direct fails with EINVAL.)
	got=$(python3 "$WORK/blk.py" read "$DEV" $((SIZE_MB << 20)) 2>&1)
	if [ "$got" = "$IMG_SUM" ]; then
		ok "all $SIZE_MB MiB read back through USB/IP (O_DIRECT) match the backing file (sha256)"
	else
		fail "direct read-back: ${got:0:200}"
		blockdev --flushbufs "$DEV" 2>/dev/null
		b=$(head -c $((SIZE_MB << 20)) "$DEV" | sha256sum | cut -d' ' -f1)
		if [ "$b" = "$IMG_SUM" ]; then info "a buffered read of $DEV does match"; else info "a buffered read of $DEV differs too"; fi
	fi
	head -c $((1 << 20)) /dev/urandom >"$WORK/patch.bin"
	if out=$(python3 "$WORK/blk.py" write "$DEV" $((4 << 20)) "$WORK/patch.bin" 2>&1); then
		a=$(sha256sum <"$WORK/patch.bin" | cut -d' ' -f1)
		b=$(python3 "$WORK/blk.py" read "$WORK/disk.img" $((1 << 20)) $((4 << 20)) 2>&1)
		if [ "$a" = "$b" ]; then ok "a 1 MiB O_DIRECT|O_SYNC write through the imported disk landed in the backing file"; else fail "the write did not reach the backing file"; fi
	else
		fail "write to $DEV: ${out:0:200}"
	fi
else
	fail "no block device appeared for $LBUSID"; dmesg | tail -n 20 | sed 's/^/         /'
fi

say "detach"
check "remohop usb detach $VPORT" client usb detach "$VPORT"
if wait_until 15 "awk -v p=$VPORT '\$2+0 == p && \$3 == \"004\" {f=1} END {exit !f}' $VHCI/status*"; then ok "vhci port $VPORT is free again"; else fail "vhci port $VPORT still in use"; fi
if wait_until 10 "[ ! -e /sys/bus/usb/devices/$LBUSID ]"; then ok "$LBUSID is gone from sysfs"; else fail "$LBUSID still in sysfs"; fi
if [ -z "${DEV:-}" ] || wait_until 10 "[ ! -b $DEV ]"; then ok "the imported disk is gone"; else fail "$DEV still exists"; fi
if client usb --json | grep -q '^\[\]'; then ok "remohop usb: nothing attached"; else fail "remohop usb still lists: $(client usb 2>&1 | head -3)"; fi
if wait_until 15 "[ \"\$(drv $BUSID)\" = usb ]"; then ok "host side: $BUSID back on its own driver (usb)"; else fail "host side: $BUSID driver '$(drv "$BUSID")'"; fi
if ! grep -qw "$BUSID" /sys/bus/usb/drivers/usbip-host/match_busid 2>/dev/null; then ok "usbip-host forgot $BUSID (match_busid)"; else fail "$BUSID still in usbip-host match_busid"; fi
if wait_until 15 "blockdev $BUSID"; then ok "the host's own usb-storage has the disk again (/dev/$(blockdev "$BUSID"))"; else info "no host-side disk after restore"; fi

say "session end gives the device back"
if attach_and_find; then
	ok "attached again (port $VPORT)"
	kill "$HOST_PID"; wait "$HOST_PID" 2>/dev/null; HOST_PID=
	if wait_until 20 "awk -v p=$VPORT '\$2+0 == p && \$3 == \"004\" {f=1} END {exit !f}' $VHCI/status*"; then ok "client: vhci port freed when the host went away"; else fail "client: port $VPORT still in use"; fi
	if wait_until 15 "[ \"\$(drv $BUSID)\" = usb ]"; then ok "host: $BUSID restored to usb when the host stopped"; else fail "host: $BUSID driver '$(drv "$BUSID")' after the host stopped"; fi
	if wait_until 10 "! client ps 2>/dev/null | grep -q usb"; then ok "the background attach ended"; else fail "background attach still running"; fi
else
	fail "second attach"; tail_of "$WORK/attach.txt"
fi

finish usbip
