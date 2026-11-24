package forward

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Spec describes one forward in the ssh -L / -R notation.
//
// For a local forward, BindHost:BindPort is where the client listens and
// Host:Port is the target as seen from the host. For a reverse forward,
// BindHost:BindPort is where the host listens and Host:Port is the target as
// seen from the client.
type Spec struct {
	BindHost string // "" listens on all interfaces
	BindPort int    // 0 picks a free port
	Host     string
	Port     int
}

// DefaultHost is the bind address and target host used when a spec omits them.
const DefaultHost = "127.0.0.1"

// ParseSpec parses a forward spec in one of these forms:
//
//	PORT:TARGETPORT                  target is 127.0.0.1:TARGETPORT
//	PORT:TARGETHOST:TARGETPORT
//	BIND:PORT:TARGETHOST:TARGETPORT
//
// IPv6 literals are written in brackets ([::1]:8080:[::1]:80). A bind of "*"
// or an empty bind listens on all interfaces. PORT may be 0 to pick a free
// port.
func ParseSpec(s string) (Spec, error) {
	parts, err := splitColons(s)
	if err != nil {
		return Spec{}, fmt.Errorf("bad forward spec %q: %v", s, err)
	}
	sp := Spec{BindHost: DefaultHost, Host: DefaultHost}
	var bind, port, host, tport string
	switch len(parts) {
	case 2:
		port, tport = parts[0], parts[1]
	case 3:
		port, host, tport = parts[0], parts[1], parts[2]
	case 4:
		bind, port, host, tport = parts[0], parts[1], parts[2], parts[3]
		sp.BindHost = bind
		if bind == "*" {
			sp.BindHost = ""
		}
	default:
		return Spec{}, fmt.Errorf("bad forward spec %q: want PORT:TARGETPORT, PORT:HOST:TARGETPORT or BIND:PORT:HOST:TARGETPORT", s)
	}
	if sp.BindPort, err = parsePort(port, true); err != nil {
		return Spec{}, fmt.Errorf("bad forward spec %q: listen port: %v", s, err)
	}
	if sp.Port, err = parsePort(tport, false); err != nil {
		return Spec{}, fmt.Errorf("bad forward spec %q: target port: %v", s, err)
	}
	if len(parts) >= 3 {
		if host == "" {
			return Spec{}, fmt.Errorf("bad forward spec %q: empty target host", s)
		}
		sp.Host = host
	}
	return sp, nil
}

// Bind is the listen address in host:port form.
func (s Spec) Bind() string { return net.JoinHostPort(s.BindHost, strconv.Itoa(s.BindPort)) }

// Target is the target address in host:port form.
func (s Spec) Target() string { return net.JoinHostPort(s.Host, strconv.Itoa(s.Port)) }

func (s Spec) String() string { return s.Bind() + " -> " + s.Target() }

func parsePort(p string, allowZero bool) (int, error) {
	n, err := strconv.Atoi(p)
	if err != nil || n < 0 || n > 65535 || (n == 0 && !allowZero) {
		return 0, fmt.Errorf("invalid port %q", p)
	}
	return n, nil
}

// splitColons splits on colons outside [...] and strips the brackets.
func splitColons(s string) ([]string, error) {
	var parts []string
	var cur strings.Builder
	inBr := false
	for _, r := range s {
		switch {
		case r == '[' && !inBr && cur.Len() == 0:
			inBr = true
		case r == ']' && inBr:
			inBr = false
		case r == '[' || r == ']':
			return nil, fmt.Errorf("misplaced bracket")
		case r == ':' && !inBr:
			parts = append(parts, cur.String())
			cur.Reset()
		case r == ' ' || r == '\t':
			return nil, fmt.Errorf("unexpected whitespace")
		default:
			cur.WriteRune(r)
		}
	}
	if inBr {
		return nil, fmt.Errorf("unterminated bracket")
	}
	return append(parts, cur.String()), nil
}
