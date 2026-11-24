// Package forward is the RemoHop port-forwarding service.
//
// Local forwards (like ssh -L) listen on the client and tunnel every accepted
// TCP connection to an address the host can reach. Reverse forwards (like
// ssh -R) make the host listen and carry each inbound connection back to the
// client, which dials a local target.
//
// Wire protocol, all on ordinary session streams:
//
//   - "forward": request {network, addr}. The host dials, answers with one
//     JSON status message {ok, error} and then both sides copy raw bytes.
//
//   - "forward.listen": request {network, bind, id}. The host listens and
//     answers {ok, error, addr}. The stream then stays open as a control
//     stream: closing it (or the session ending) closes the listener.
//
//   - "forward.accept": opened by the host towards the client for every
//     connection the reverse listener accepts, request {id}. The client dials
//     the target registered under id, answers with a status message and the
//     two sides copy raw bytes.
//
//   - "forward.udp": one stream per UDP forward; see udp.go. Packets travel
//     as QUIC datagrams when they fit and as frames on that stream otherwise.
//
// A running `remohop forward` process also writes a small record next to its
// internal/bg entry and listens on a token-protected loopback control
// socket, which is what `remohop forward` (list) and `remohop unforward` use.
//
// Every TCP connection uses its own stream. A FIN on one side becomes
// CloseWrite on the other so half-close survives the tunnel; any other error
// resets the stream and the TCP connection.
package forward
