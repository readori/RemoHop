// Command keyseed writes a remohop identity key (PKCS #8 PEM, the format of
// `remohop key import`) derived from a 32-byte seed given in hex in $SEED.
//
// The internet e2e workflow uses it so that jobs on different runners agree
// on a host's identity (and so on its 9-digit ID) without passing the key
// between jobs: each derives it from a secret they share.
package main

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: SEED=<64 hex digits> keyseed OUTFILE")
		os.Exit(2)
	}
	seed, err := hex.DecodeString(os.Getenv("SEED"))
	if err != nil || len(seed) != ed25519.SeedSize {
		fmt.Fprintln(os.Stderr, "keyseed: SEED must be 64 hex digits")
		os.Exit(2)
	}
	der, err := x509.MarshalPKCS8PrivateKey(ed25519.NewKeyFromSeed(seed))
	if err != nil {
		fmt.Fprintln(os.Stderr, "keyseed:", err)
		os.Exit(1)
	}
	out := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(os.Args[1], out, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "keyseed:", err)
		os.Exit(1)
	}
}
