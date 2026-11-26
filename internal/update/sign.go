package update

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// In-app upgrades trust a release only through an Ed25519 signature made
// by the release workflow (secret UPDATE_SIGNING_KEY) over the release's
// SHA256SUMS. The signed message binds the tag, so the files of an older
// release cannot be passed off as a newer one:
//
//	"remohop-update-v1\n" + tag + "\n" + the bytes of SHA256SUMS
//
// The signature is the release asset SigFile: base64 on one line. The
// public keys the app accepts are TrustedKeys (keys.go); with none, one-click
// upgrades are off and the app only opens the download page.
const (
	// AppleTeamID signs the macOS app (checked by VerifyBundle).
	AppleTeamID = "H7FLAAL9RB"
	SigContext  = "remohop-update-v1"
	SumsFile    = "SHA256SUMS"
	SigFile     = "SHA256SUMS.ed25519"
)

// Message is what is signed for a release.
func Message(tag string, sums []byte) []byte {
	m := make([]byte, 0, len(SigContext)+len(tag)+2+len(sums))
	m = append(m, SigContext...)
	m = append(m, '\n')
	m = append(m, tag...)
	m = append(m, '\n')
	return append(m, sums...)
}

// Sign signs a release's SHA256SUMS; the result is the content of SigFile.
func Sign(priv ed25519.PrivateKey, tag string, sums []byte) []byte {
	sig := ed25519.Sign(priv, Message(tag, sums))
	return []byte(base64.StdEncoding.EncodeToString(sig) + "\n")
}

// ErrBadSignature: the signature is not one of a trusted key over these
// sums for this tag.
var ErrBadSignature = errors.New("the release signature does not check out")

// Verify checks sig (the content of SigFile) against the trusted keys.
func Verify(keys []ed25519.PublicKey, tag string, sums, sig []byte) error {
	if len(keys) == 0 {
		return errors.New("no trusted update keys in this build")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil || len(raw) != ed25519.SignatureSize {
		return ErrBadSignature
	}
	msg := Message(tag, sums)
	for _, k := range keys {
		if ed25519.Verify(k, msg, raw) {
			return nil
		}
	}
	return ErrBadSignature
}

// ParseKey reads a public key written as base64 (what `updsign keygen`
// prints).
func ParseKey(s string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("not an Ed25519 public key: %q", s)
	}
	return ed25519.PublicKey(b), nil
}

// Keys returns TrustedKeys parsed. A key that does not parse is a build
// mistake and panics (TestTrustedKeys catches it first).
func Keys() []ed25519.PublicKey {
	out := make([]ed25519.PublicKey, 0, len(TrustedKeys))
	for _, s := range TrustedKeys {
		k, err := ParseKey(s)
		if err != nil {
			panic(err)
		}
		out = append(out, k)
	}
	return out
}

// ParseSums reads SHA256SUMS ("<hex>  <name>" lines) into name -> hex.
func ParseSums(b []byte) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 2 || len(f[0]) != 64 {
			return nil, fmt.Errorf("SHA256SUMS: bad line %q", line)
		}
		out[strings.TrimPrefix(f[1], "*")] = strings.ToLower(f[0])
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errors.New("SHA256SUMS: empty")
	}
	return out, nil
}
