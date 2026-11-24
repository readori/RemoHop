// Package usb exports USB devices of the host to the client over the
// session using the Linux USB/IP kernel drivers (Linux on both sides, root on
// both sides).
//
// The host binds an allowed device to the usbip-host driver; the client
// imports it through vhci_hcd. Both kernel drivers speak the standard USB/IP
// protocol on a socket, so each side makes a loopback TCP connection, hands
// one end to the kernel (usbip_sockfd on the host, vhci_hcd attach on the
// client) and relays the other end over a session stream.
//
// Wire protocol, service "usb":
//
//   - request {op:"list"}: the host answers one JSON {ok, error, note,
//     devices:[...]} (a rich listing with strings and an allowed flag) and
//     ends the stream.
//   - request {op:"usbip"}: the stream then carries the standard USB/IP
//     protocol (version 0x0111): OP_REQ_DEVLIST gets OP_REP_DEVLIST (the
//     devices the host owner allowed); OP_REQ_IMPORT with a busid gets
//     OP_REP_IMPORT, and after a successful import the stream carries the raw
//     URB traffic of the two kernels until either side closes it. An
//     extension: after a refusing OP_REP_IMPORT status the host appends one
//     JSON message {error} explaining why (a standard client ignores it).
//
// Exporting detaches a device from its host driver, so a device is exported
// only if the host owner allowed that busid and VID:PID with `remohop usb allow`,
// and the original driver is rebound when the client detaches or the session
// ends.
package usb
