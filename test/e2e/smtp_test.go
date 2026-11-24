package e2e

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// testCA is a throwaway certificate authority for local stand-ins (the SMTP
// server, the TLS proxy that plays the tunnel).
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newTestCA(t *testing.T) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "RemoHop e2e test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return &testCA{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// issue makes a server certificate for names (DNS names or IP literals).
func (ca *testCA) issue(t *testing.T, names ...string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: names[0]},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, n := range names {
		if ip := net.ParseIP(n); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, n)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func (ca *testCA) pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(ca.cert)
	return p
}

func (ca *testCA) writePEM(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(p, ca.pem, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// mail is one message the stand-in SMTP server accepted.
type mail struct {
	From, To string
	Data     string
	TLS      bool // the transaction ran over STARTTLS
	Authed   bool
}

// smtpServer is a small ESMTP server for running the accounts test where
// no real SMTP server is available. It speaks just what net/smtp uses:
// EHLO, STARTTLS (required before MAIL), AUTH PLAIN, MAIL, RCPT, DATA, QUIT.
type smtpServer struct {
	ln         net.Listener
	tls        *tls.Config
	user, pass string

	mu    sync.Mutex
	mails []mail
}

func startSMTP(t *testing.T, cert tls.Certificate, user, pass string) *smtpServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &smtpServer{ln: ln, tls: &tls.Config{Certificates: []tls.Certificate{cert}}, user: user, pass: pass}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	return s
}

func (s *smtpServer) port() int { return s.ln.Addr().(*net.TCPAddr).Port }

func (s *smtpServer) all() []mail {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]mail(nil), s.mails...)
}

func (s *smtpServer) serve(c net.Conn) {
	defer func() { c.Close() }()
	c.SetDeadline(time.Now().Add(time.Minute))
	r := bufio.NewReader(c)
	w := func(line string) { c.Write([]byte(line + "\r\n")) }
	w("220 remohop-e2e ESMTP")
	isTLS, authed := false, false
	var cur mail
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		verb := strings.ToUpper(strings.SplitN(line, " ", 2)[0])
		switch verb {
		case "EHLO", "HELO":
			if isTLS {
				w("250-remohop-e2e")
				w("250 AUTH PLAIN")
			} else {
				w("250-remohop-e2e")
				w("250 STARTTLS")
			}
		case "STARTTLS":
			if isTLS {
				w("503 already in TLS")
				continue
			}
			w("220 go ahead")
			tc := tls.Server(c, s.tls)
			if tc.Handshake() != nil {
				return
			}
			c, r, isTLS = tc, bufio.NewReader(tc), true
			w = func(line string) { tc.Write([]byte(line + "\r\n")) }
		case "AUTH":
			f := strings.Fields(line)
			if !isTLS || len(f) != 3 || !strings.EqualFold(f[1], "PLAIN") {
				w("504 AUTH PLAIN with an initial response, over TLS")
				continue
			}
			b, _ := base64.StdEncoding.DecodeString(f[2])
			parts := strings.Split(string(b), "\x00")
			if len(parts) == 3 && parts[1] == s.user && parts[2] == s.pass {
				authed = true
				w("235 ok")
			} else {
				w("535 bad credentials")
			}
		case "MAIL":
			if !isTLS {
				w("530 must issue STARTTLS first")
				continue
			}
			if s.user != "" && !authed {
				w("530 authentication required")
				continue
			}
			cur = mail{From: addrOf(line), TLS: isTLS, Authed: authed}
			w("250 ok")
		case "RCPT":
			cur.To = addrOf(line)
			w("250 ok")
		case "DATA":
			w("354 end with .")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(strings.TrimPrefix(l, "."))
			}
			cur.Data = b.String()
			s.mu.Lock()
			s.mails = append(s.mails, cur)
			s.mu.Unlock()
			w("250 queued")
		case "RSET", "NOOP":
			w("250 ok")
		case "QUIT":
			w("221 bye")
			return
		default:
			w("502 not implemented")
		}
	}
}

func addrOf(line string) string {
	if i, j := strings.Index(line, "<"), strings.LastIndex(line, ">"); i >= 0 && j > i {
		return line[i+1 : j]
	}
	return ""
}
