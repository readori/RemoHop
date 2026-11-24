// Package auth is RemoHop's authentication layer: Ed25519 identity keys, the
// password / pairing-token handshake (CPace bound to the TLS channel) that
// turns an encrypted-but-anonymous QUIC connection into an authenticated
// session, the host-side brute-force limiter and the pairing-token store.
//
// Trust model. Peers have no PKI: QUIC's TLS 1.3 runs with self-signed
// certificates, so on its own it only stops passive eavesdroppers. The
// rendezvous server that introduced the peers is untrusted too. What
// authenticates a session is knowledge of a shared credential, proven with a
// balanced PAKE whose inputs include this exact TLS connection's exporter
// (see handshake.go). Identity keys add stable names on top: the client pins
// the host's key after the first successful handshake, and pairing tokens
// are bound to the client key they were issued to.
package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/readori/RemoHop/internal/config"
)

// IdentityFile is the name of the identity key inside the config directory.
const IdentityFile = "identity.key"

// Identity is a long-lived Ed25519 key pair naming one RemoHop installation.
type Identity struct {
	priv ed25519.PrivateKey
}

// NewIdentity generates a fresh identity that is not stored anywhere.
func NewIdentity() (*Identity, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &Identity{priv: priv}, nil
}

// LoadIdentity returns the identity stored in dir, creating it on first use.
func LoadIdentity(dir string) (*Identity, error) {
	return LoadIdentityFile(filepath.Join(dir, IdentityFile))
}

// LoadIdentityFile returns the identity stored at path (PEM, PKCS #8, mode
// 0600), creating it on first use.
func LoadIdentityFile(path string) (*Identity, error) {
	// The directory that holds the key holds every other secret of this
	// installation too (tokens, saved peers, the audit log, the control
	// socket). Where permission bits do not exist, privacy is a property
	// of the directory; see config.MakePrivate.
	private := config.MakePrivate(filepath.Dir(path))
	if id, err := readIdentity(path); err == nil {
		return id, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if private != nil {
		// An existing key is used where it lies, but a new one is not
		// written into a directory others may read.
		return nil, fmt.Errorf("cannot make %s private to this user: %w", filepath.Dir(path), private)
	}
	// Two processes may start at once on a fresh machine; the lock makes
	// them agree on one key instead of each keeping its own.
	unlock, err := config.Lock(path)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if id, err := readIdentity(path); err == nil {
		return id, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	id, err := NewIdentity()
	if err != nil {
		return nil, err
	}
	if err := writeIdentity(path, id); err != nil {
		return nil, err
	}
	return id, nil
}

// MarshalPrivate renders the identity's private key as PEM (PKCS #8), the
// format of the identity file. It is the secret that every pairing,
// authorised key and pinned host key of this installation rests on.
func (id *Identity) MarshalPrivate() ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(id.priv)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// ParseIdentity reads a private key in the format MarshalPrivate writes.
func ParseIdentity(blob []byte) (*Identity, error) {
	block, _ := pem.Decode(blob)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, errors.New("not a PEM private key")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	priv, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("not an Ed25519 key")
	}
	return &Identity{priv: priv}, nil
}

func writeIdentity(path string, id *Identity) error {
	blob, err := id.MarshalPrivate()
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(path, blob, 0o600)
}

// ReplaceIdentity installs id as the identity stored in dir, replacing any
// existing one; nil removes the identity, so the next use creates a new
// one. Processes already running keep the key they loaded.
func ReplaceIdentity(dir string, id *Identity) error {
	path := filepath.Join(dir, IdentityFile)
	if err := config.MakePrivate(dir); err != nil {
		return fmt.Errorf("cannot make %s private to this user: %w", dir, err)
	}
	unlock, err := config.Lock(path)
	if err != nil {
		return err
	}
	defer unlock()
	if id == nil {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	return writeIdentity(path, id)
}

func readIdentity(path string) (*Identity, error) {
	b, err := config.ReadFile(path)
	if err != nil {
		return nil, err
	}
	id, err := ParseIdentity(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return id, nil
}

// Public returns the identity's public key.
func (id *Identity) Public() ed25519.PublicKey {
	return id.priv.Public().(ed25519.PublicKey)
}

// Sign signs msg with the identity key.
func (id *Identity) Sign(msg []byte) []byte { return ed25519.Sign(id.priv, msg) }

// certificate makes a throwaway self-signed certificate for the key. Nobody
// validates the certificate itself (no names, no chain, no dates): it is
// only the envelope TLS needs to carry the public key and prove possession
// of the private key in CertificateVerify.
func (id *Identity) certificate() (tls.Certificate, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		NotBefore:    time.Now().Add(-24 * time.Hour),
		NotAfter:     time.Now().Add(10 * 365 * 24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, id.Public(), id.priv)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: id.priv}, nil
}

// ServerTLS returns the TLS configuration for the accepting end of a QUIC
// connection that presents this identity. With requireClientKey the dialling
// side must present an identity too (it is not verified against anything
// here; callers read it back with PeerKey).
func (id *Identity) ServerTLS(alpn string, requireClientKey bool) (*tls.Config, error) {
	cert, err := id.certificate()
	if err != nil {
		return nil, err
	}
	conf := &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{alpn},
		MinVersion:   tls.VersionTLS13,
		// No resumption: every connection gets a full handshake, so the
		// exporter the PAKE binds to is always tied to both certificates.
		SessionTicketsDisabled: true,
	}
	if requireClientKey {
		conf.ClientAuth = tls.RequireAnyClientCert
	}
	return conf, nil
}

// ClientTLS returns the TLS configuration for the dialling end. verify, if
// not nil, is called during the handshake with the key the server presented
// and can reject it (identity pinning). Without it any key is accepted:
// authentication is the job of the PAKE that follows, not of the certificate.
func (id *Identity) ClientTLS(alpn string, verify func(ed25519.PublicKey) error) (*tls.Config, error) {
	cert, err := id.certificate()
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{alpn},
		MinVersion:   tls.VersionTLS13,
		// Chain and name checks are meaningless for self-signed peers; the
		// TLS stack still verifies that the peer holds the presented key.
		InsecureSkipVerify:    true,
		VerifyPeerCertificate: VerifyPeerKey(verify),
	}, nil
}

// VerifyPeerKey returns a tls.Config.VerifyPeerCertificate callback that
// requires the peer to present an Ed25519 identity and, if verify is not
// nil, passes that key to it for acceptance.
func VerifyPeerKey(verify func(ed25519.PublicKey) error) func([][]byte, [][]*x509.Certificate) error {
	return func(raw [][]byte, _ [][]*x509.Certificate) error {
		if len(raw) == 0 {
			return errors.New("peer presented no identity")
		}
		cert, err := x509.ParseCertificate(raw[0])
		if err != nil {
			return fmt.Errorf("peer identity: %w", err)
		}
		key, err := keyFromCert(cert)
		if err != nil {
			return err
		}
		if verify != nil {
			return verify(key)
		}
		return nil
	}
}

func keyFromCert(cert *x509.Certificate) (ed25519.PublicKey, error) {
	key, ok := cert.PublicKey.(ed25519.PublicKey)
	if !ok || len(key) != ed25519.PublicKeySize {
		return nil, errors.New("peer identity is not an Ed25519 key")
	}
	return key, nil
}

// PeerKey extracts the identity key the remote side proved possession of in
// the TLS handshake.
func PeerKey(cs tls.ConnectionState) (ed25519.PublicKey, error) {
	if len(cs.PeerCertificates) == 0 {
		return nil, errors.New("peer presented no identity")
	}
	return keyFromCert(cs.PeerCertificates[0])
}

// EncodeKey renders a public key for config files and wire messages.
func EncodeKey(pub ed25519.PublicKey) string {
	return base64.RawStdEncoding.EncodeToString(pub)
}

// DecodeKey parses the output of EncodeKey.
func DecodeKey(s string) (ed25519.PublicKey, error) {
	b, err := base64.RawStdEncoding.DecodeString(s)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("malformed public key")
	}
	return ed25519.PublicKey(b), nil
}

// Fingerprint is a short human-comparable name for a public key: the first
// 80 bits of its SHA-256 in hex groups, e.g. "3f9a:01bc:77d2:e410:9c55".
func Fingerprint(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	h := hex.EncodeToString(sum[:10])
	out := make([]byte, 0, 24)
	for i := 0; i < len(h); i += 4 {
		if i > 0 {
			out = append(out, ':')
		}
		out = append(out, h[i:i+4]...)
	}
	return string(out)
}

// keyLinePrefix starts the one-line text form of a public identity key.
const keyLinePrefix = "remohop-key"

// FormatKeyLine renders a public key as the line `remohop key export` prints
// and `remohop host authorize` / `remohop peers trust` accept:
// "remohop-key <base64 key> [label]".
func FormatKeyLine(pub ed25519.PublicKey, label string) string {
	line := keyLinePrefix + " " + EncodeKey(pub)
	if label = CleanText(label, MaxTokenName); label != "" {
		line += " " + label
	}
	return line
}

// ParseKeyLine parses a line made by FormatKeyLine. A bare base64 key is
// accepted too.
func ParseKeyLine(line string) (pub ed25519.PublicKey, label string, err error) {
	fields := strings.Fields(line)
	if len(fields) > 0 && fields[0] == keyLinePrefix {
		fields = fields[1:]
	}
	if len(fields) == 0 {
		return nil, "", errors.New("empty key line")
	}
	if pub, err = DecodeKey(fields[0]); err != nil {
		return nil, "", errors.New("not a remohop public key (expected the line printed by `remohop key export`)")
	}
	return pub, CleanText(strings.Join(fields[1:], " "), MaxTokenName), nil
}
