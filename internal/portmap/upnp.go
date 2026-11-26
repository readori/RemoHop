package portmap

import (
	"bufio"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// UPnP Internet Gateway Device: find the router with an SSDP search, fetch
// its device description, and talk SOAP to its WAN connection service.

const (
	// maxDescription bounds a device description or SOAP response; real
	// ones are a few KiB.
	maxDescription = 64 << 10
	// maxSSDPAnswers bounds how many SSDP answers one discovery looks at.
	maxSSDPAnswers = 16
	// httpTimeout bounds each HTTP request to the router.
	httpTimeout = 2 * time.Second

	faultConflict       = 718 // ConflictInMappingEntry
	faultPermanentLease = 725 // OnlyPermanentLeasesSupported
)

// wanServices are the service types that can map a port, most preferred
// first.
var wanServices = []string{
	"urn:schemas-upnp-org:service:WANIPConnection:2",
	"urn:schemas-upnp-org:service:WANIPConnection:1",
	"urn:schemas-upnp-org:service:WANPPPConnection:1",
}

// searchTargets are what an M-SEARCH asks for: the WAN services, and the
// gateway device itself, which some routers answer only to.
var searchTargets = append(append([]string(nil), wanServices...), "urn:schemas-upnp-org:device:InternetGatewayDevice:1")

// igd is a UPnP gateway found on the LAN.
type igd struct {
	ip       netip.Addr // the device's address, the host of every URL used
	location string
	services []wanService // in order of preference
}

type wanService struct {
	typ, control string
}

func ssdpSearch(st string, to netip.AddrPort) []byte {
	return []byte("M-SEARCH * HTTP/1.1\r\n" +
		"HOST: " + to.String() + "\r\n" +
		"ST: " + st + "\r\n" +
		"MAN: \"ssdp:discover\"\r\n" +
		"MX: 1\r\n\r\n")
}

// parseSSDP reads an SSDP answer, returning its LOCATION.
func parseSSDP(p []byte) (string, error) {
	resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(p)), nil)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", errors.New("no LOCATION")
	}
	return loc, nil
}

// lanURL checks a URL a device gave us before anything is fetched from
// it: plain HTTP to a literal IP address on the LAN, the address of the
// device that answered (a host name could resolve anywhere; another
// address would let one LAN device point us at another, or at a public
// server). It returns the URL parsed.
func (m *Mapper) lanURL(raw string, device netip.Addr) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "http" || u.User != nil {
		return nil, fmt.Errorf("refusing URL %q: not plain http", raw)
	}
	ip, err := netip.ParseAddr(u.Hostname())
	if err != nil || ip.Zone() != "" {
		return nil, fmt.Errorf("refusing URL %q: not an IP address", raw)
	}
	ip = ip.Unmap()
	if ip != device || !m.env.lanOK(ip) {
		return nil, fmt.Errorf("refusing URL %q: not the LAN device %s that answered", raw, device)
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || n <= 0 || n > 65535 {
			return nil, fmt.Errorf("refusing URL %q: bad port", raw)
		}
	}
	return u, nil
}

// discoverIGD searches for a gateway with SSDP and returns the first whose
// description offers a WAN connection service.
func (m *Mapper) discoverIGD(ctx context.Context) (*igd, error) {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return nil, err
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { c.SetReadDeadline(time.Now()) })
	defer stop()

	send := func() error {
		to := net.UDPAddrFromAddrPort(m.env.ssdp)
		for _, st := range searchTargets {
			if _, err := c.WriteToUDP(ssdpSearch(st, m.env.ssdp), to); err != nil {
				return err
			}
		}
		return nil
	}
	if err := send(); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(m.env.ssdpWait)
	resend := time.Now().Add(m.env.ssdpWait / 3) // once more, for a lost packet
	tried := map[string]bool{}
	var lastErr error
	buf := make([]byte, 2048)
	for answers := 0; answers < maxSSDPAnswers; {
		if ctx.Err() != nil {
			break
		}
		next := deadline
		if !resend.IsZero() {
			next = resend
		}
		c.SetReadDeadline(next)
		n, from, err := c.ReadFromUDPAddrPort(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() && !resend.IsZero() && ctx.Err() == nil {
				resend = time.Time{}
				if err := send(); err != nil {
					return nil, err
				}
				continue
			}
			break
		}
		src := from.Addr().Unmap()
		if !m.env.lanOK(src) {
			continue // not from the LAN: not a router of ours
		}
		answers++
		loc, err := parseSSDP(buf[:n])
		if err != nil || tried[loc] {
			continue
		}
		tried[loc] = true
		dev, err := m.describe(ctx, loc, src)
		if err != nil {
			lastErr = err
			m.log.Debug("router port mapping: UPnP device unusable", "location", loc, "err", err)
			continue
		}
		return dev, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("no UPnP gateway answered")
}

// The parts of a UPnP device description that matter here.
type xmlRoot struct {
	URLBase string    `xml:"URLBase"`
	Device  xmlDevice `xml:"device"`
}

type xmlDevice struct {
	Services []xmlService `xml:"serviceList>service"`
	Devices  []xmlDevice  `xml:"deviceList>device"`
}

type xmlService struct {
	Type    string `xml:"serviceType"`
	Control string `xml:"controlURL"`
}

func (d *xmlDevice) walk(f func(xmlService)) {
	for _, s := range d.Services {
		f(s)
	}
	for i := range d.Devices {
		d.Devices[i].walk(f)
	}
}

// parseDescription finds the WAN connection services in a device
// description, in order of preference, with their control URLs as given.
func parseDescription(body []byte) (urlBase string, services []wanService, err error) {
	var root xmlRoot
	if err := xml.Unmarshal(body, &root); err != nil {
		return "", nil, err
	}
	for _, want := range wanServices {
		root.Device.walk(func(s xmlService) {
			if strings.TrimSpace(s.Type) == want && strings.TrimSpace(s.Control) != "" {
				services = append(services, wanService{typ: want, control: strings.TrimSpace(s.Control)})
			}
		})
	}
	if len(services) == 0 {
		return "", nil, errors.New("no WAN connection service in its description")
	}
	return strings.TrimSpace(root.URLBase), services, nil
}

// describe fetches and reads the description at location, which device
// sent us.
func (m *Mapper) describe(ctx context.Context, location string, device netip.Addr) (*igd, error) {
	u, err := m.lanURL(location, device)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := m.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("description: status %d", resp.StatusCode)
	}
	body, err := readBounded(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("description: %w", err)
	}
	base, services, err := parseDescription(body)
	if err != nil {
		return nil, err
	}
	baseURL := u
	if base != "" {
		if baseURL, err = m.lanURL(base, device); err != nil {
			return nil, err
		}
	}
	dev := &igd{ip: device, location: location}
	for _, s := range services {
		ref, err := url.Parse(s.control)
		if err != nil {
			continue
		}
		cu, err := m.lanURL(baseURL.ResolveReference(ref).String(), device)
		if err != nil {
			m.log.Debug("router port mapping: UPnP control URL refused", "err", err)
			continue
		}
		dev.services = append(dev.services, wanService{typ: s.typ, control: cu.String()})
	}
	if len(dev.services) == 0 {
		return nil, errors.New("no usable control URL in its description")
	}
	return dev, nil
}

func readBounded(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxDescription+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxDescription {
		return nil, errors.New("response too large")
	}
	return b, nil
}

// httpClient talks to the router: no proxy (a proxy from the environment
// cannot reach the LAN, and must not learn about it), no redirects, no
// connection reuse, short timeouts, and connections only to LAN
// addresses whatever URL is asked for.
func (m *Mapper) httpClient() *http.Client {
	dialer := &net.Dialer{Timeout: time.Second}
	return &http.Client{
		Transport: &http.Transport{
			Proxy:             nil,
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				ap, err := netip.ParseAddrPort(addr)
				if err != nil || !m.env.lanOK(ap.Addr().Unmap()) {
					return nil, fmt.Errorf("refusing to connect to %s", addr)
				}
				return dialer.DialContext(ctx, "tcp4", addr)
			},
			ResponseHeaderTimeout:  httpTimeout,
			MaxResponseHeaderBytes: 16 << 10,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       httpTimeout,
	}
}

// soapFault is an error a UPnP action returned.
type soapFault struct {
	code int
	desc string
}

func (f *soapFault) Error() string {
	if f.desc != "" {
		return fmt.Sprintf("UPnP error %d (%s)", f.code, f.desc)
	}
	return fmt.Sprintf("UPnP error %d", f.code)
}

func soapBody(serviceType, action string, args [][2]string) []byte {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0"?>` + "\r\n" +
		`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">` +
		`<s:Body><u:` + action + ` xmlns:u="`)
	xml.EscapeText(&b, []byte(serviceType))
	b.WriteString(`">`)
	for _, a := range args {
		b.WriteString("<" + a[0] + ">")
		xml.EscapeText(&b, []byte(a[1]))
		b.WriteString("</" + a[0] + ">")
	}
	b.WriteString(`</u:` + action + `></s:Body></s:Envelope>`)
	return b.Bytes()
}

// soapFields collects the text of the leaf elements of a SOAP response by
// local name (the first of each name).
func soapFields(body []byte) (map[string]string, error) {
	d := xml.NewDecoder(bytes.NewReader(body))
	out := map[string]string{}
	var name string
	var text strings.Builder
	for {
		tok, err := d.Token()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			name = t.Name.Local
			text.Reset()
		case xml.CharData:
			if name != "" && text.Len() < 1024 {
				text.Write(t)
			}
		case xml.EndElement:
			if name == t.Name.Local {
				if _, seen := out[name]; !seen {
					out[name] = strings.TrimSpace(text.String())
				}
			}
			name = ""
		}
	}
}

// soap calls action on a WAN connection service.
func (m *Mapper) soap(ctx context.Context, svc wanService, action string, args [][2]string) (map[string]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, svc.control, bytes.NewReader(soapBody(svc.typ, action, args)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("SOAPAction", `"`+svc.typ+"#"+action+`"`)
	resp, err := m.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := readBounded(resp.Body)
	if err != nil {
		return nil, err
	}
	fields, perr := soapFields(body)
	if resp.StatusCode != http.StatusOK {
		if perr == nil && fields["errorCode"] != "" {
			code, _ := strconv.Atoi(fields["errorCode"])
			return nil, &soapFault{code: code, desc: fields["errorDescription"]}
		}
		return nil, fmt.Errorf("%s: status %d", action, resp.StatusCode)
	}
	if perr != nil {
		return nil, fmt.Errorf("%s: %w", action, perr)
	}
	return fields, nil
}

// upnpLease is a UPnP port mapping.
type upnpLease struct {
	m        *Mapper
	dev      *igd
	svc      wanService
	client   netip.Addr // our LAN address, NewInternalClient
	external netip.AddrPort
	lease    uint32 // seconds; 0 when the router takes only permanent mappings
}

func (m *Mapper) mapUPnP(ctx context.Context, dev *igd) (Mapping, lease, error) {
	client, err := localIPFor(dev.ip)
	if err != nil {
		return Mapping{}, nil, err
	}
	var errs []error
	for _, svc := range dev.services {
		l := &upnpLease{m: m, dev: dev, svc: svc, client: client, lease: uint32(m.env.lifetime / time.Second)}
		cur, err := l.add(ctx, m.cfg.Port)
		if err == nil {
			return cur, l, nil
		}
		errs = append(errs, err)
		if ctx.Err() != nil {
			break
		}
	}
	return Mapping{}, nil, errors.Join(errs...)
}

// externalIP asks the service for the router's public address.
func (l *upnpLease) externalIP(ctx context.Context) (netip.Addr, error) {
	f, err := l.m.soap(ctx, l.svc, "GetExternalIPAddress", nil)
	if err != nil {
		return netip.Addr{}, err
	}
	ip, err := netip.ParseAddr(f["NewExternalIPAddress"])
	if err != nil || !ip.Unmap().Is4() || ip.IsUnspecified() {
		return netip.Addr{}, fmt.Errorf("no usable external address (%q)", f["NewExternalIPAddress"])
	}
	return ip.Unmap(), nil
}

func (l *upnpLease) addPortMapping(ctx context.Context, port uint16) error {
	_, err := l.m.soap(ctx, l.svc, "AddPortMapping", [][2]string{
		{"NewRemoteHost", ""},
		{"NewExternalPort", strconv.Itoa(int(port))},
		{"NewProtocol", "UDP"},
		{"NewInternalPort", strconv.Itoa(int(l.m.cfg.Port))},
		{"NewInternalClient", l.client.String()},
		{"NewEnabled", "1"},
		{"NewPortMappingDescription", description},
		{"NewLeaseDuration", strconv.FormatUint(uint64(l.lease), 10)},
	})
	return err
}

// add maps an external port, port if the router lets us: another port
// when that one is taken, a permanent mapping when it refuses a lease.
func (l *upnpLease) add(ctx context.Context, port uint16) (Mapping, error) {
	ext, err := l.externalIP(ctx)
	if err != nil {
		return Mapping{}, err
	}
	for tries := 0; ; tries++ {
		err = l.addPortMapping(ctx, port)
		if err == nil {
			break
		}
		var f *soapFault
		if tries == 3 || ctx.Err() != nil || !errors.As(err, &f) {
			return Mapping{}, err
		}
		switch {
		case f.code == faultConflict:
			port = uint16(1024 + rand.IntN(65536-1024)) // taken by another machine
		case l.lease != 0:
			// Some routers take only permanent mappings: 725, or
			// whatever error they chose for it.
			l.lease = 0
		default:
			return Mapping{}, err
		}
	}
	l.external = netip.AddrPortFrom(ext, port)
	return Mapping{
		Protocol: ProtoUPnP,
		External: l.external,
		Internal: netip.AddrPortFrom(l.client, l.m.cfg.Port),
		Lifetime: time.Duration(l.lease) * time.Second,
	}, nil
}

func (l *upnpLease) renew(ctx context.Context) (Mapping, error) {
	if local, err := localIPFor(l.dev.ip); err != nil {
		return Mapping{}, err
	} else if local != l.client {
		return Mapping{}, fmt.Errorf("this machine's address changed from %s to %s", l.client, local)
	}
	ext, err := l.externalIP(ctx)
	if err != nil {
		return Mapping{}, err
	}
	if err := l.addPortMapping(ctx, l.external.Port()); err != nil {
		return Mapping{}, err
	}
	l.external = netip.AddrPortFrom(ext, l.external.Port())
	return Mapping{
		Protocol: ProtoUPnP,
		External: l.external,
		Internal: netip.AddrPortFrom(l.client, l.m.cfg.Port),
		Lifetime: time.Duration(l.lease) * time.Second,
	}, nil
}

func (l *upnpLease) remove(ctx context.Context) error {
	_, err := l.m.soap(ctx, l.svc, "DeletePortMapping", [][2]string{
		{"NewRemoteHost", ""},
		{"NewExternalPort", strconv.Itoa(int(l.external.Port()))},
		{"NewProtocol", "UDP"},
	})
	return err
}
