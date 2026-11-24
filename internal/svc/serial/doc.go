// Package serial carries a serial port of the host to the client as a local
// virtual serial port.
//
// Wire protocol, on one session stream named "serial":
//
//   - request {op, port, baud, data, parity, stop, dtr, rts}. op "list"
//     answers one JSON {ok, error, ports:[...]} and ends. op "open" opens the
//     port on the host (exclusively, raw, 8N1 unless asked otherwise) and
//     answers {ok, error, port, baud}.
//   - after an ok open both sides exchange binary frames (the wire package
//     framing, payload at most 64 KiB): 1 DATA raw bytes; 2 CONFIG JSON
//     {baud,data,parity,stop,rtscts} client to host; 3 MODEM JSON, client to
//     host {dtr,rts} (null = unchanged), host to client {cts,dsr,dcd,ri};
//     4 BREAK client to host, 2-byte big-endian milliseconds; 5 NOTICE host
//     to client, a text diagnostic (for example a refused CONFIG).
//   - the stream ending, in either direction, closes the host port.
//
// The client end of a virtual port is a pseudo-terminal whose slave is
// symlinked to a path of the user's choosing; it exists on Linux and macOS.
package serial
