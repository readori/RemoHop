// Command updsign signs a release's SHA256SUMS for in-app upgrades and
// makes the Ed25519 key pairs for it (internal/update/sign.go).
//
//	updsign keygen DIR
//	    writes DIR/update-signing.key (the private key, base64; mode 0600)
//	    and prints the public key, for internal/update/keys.go. Run it on
//	    the machine that sets the repository secret, never in CI:
//	    gh secret set UPDATE_SIGNING_KEY -R readori/RemoHop < DIR/update-signing.key
//	updsign sign -tag vX.Y.Z -in SHA256SUMS -out SHA256SUMS.ed25519
//	    signs with the private key in $UPDATE_SIGNING_KEY
//	updsign verify -tag vX.Y.Z -in SHA256SUMS -sig SHA256SUMS.ed25519
//	    checks the signature against the keys this build trusts
//	updsign keys
//	    exits 0 when this build trusts any key (one-click upgrades on),
//	    1 when it trusts none
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/readori/RemoHop/internal/update"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "updsign:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: updsign keygen DIR | sign | verify | keys")
	}
	switch args[0] {
	case "keygen":
		if len(args) != 2 {
			return errors.New("usage: updsign keygen DIR")
		}
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		path := filepath.Join(args[1], "update-signing.key")
		if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(priv.Seed())+"\n"), 0o600); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "private key written to %s (keep it secret; set it as UPDATE_SIGNING_KEY)\n", path)
		fmt.Println(base64.StdEncoding.EncodeToString(pub))
		return nil
	case "keys":
		if len(update.Keys()) == 0 {
			os.Exit(1)
		}
		return nil
	case "sign", "verify":
		fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
		tag := fs.String("tag", "", "release tag")
		in := fs.String("in", "", "SHA256SUMS")
		out := fs.String("out", "", "signature file to write (sign)")
		sigPath := fs.String("sig", "", "signature file to check (verify)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if v, ok := update.ParseVersion(*tag); !ok || v.Pre != "" {
			return fmt.Errorf("-tag %q is not a release version", *tag)
		}
		sums, err := os.ReadFile(*in)
		if err != nil {
			return err
		}
		if args[0] == "verify" {
			sig, err := os.ReadFile(*sigPath)
			if err != nil {
				return err
			}
			if err := update.Verify(update.Keys(), *tag, sums, sig); err != nil {
				return err
			}
			fmt.Fprintln(os.Stderr, "signature OK")
			return nil
		}
		priv, err := privateKey(os.Getenv("UPDATE_SIGNING_KEY"))
		if err != nil {
			return err
		}
		return os.WriteFile(*out, update.Sign(priv, *tag, sums), 0o644)
	}
	return fmt.Errorf("unknown command %q", args[0])
}

// privateKey reads a key as keygen writes it (a base64 seed), or a full
// 64-byte private key in base64.
func privateKey(s string) (ed25519.PrivateKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	switch {
	case err != nil:
		return nil, errors.New("UPDATE_SIGNING_KEY is not base64")
	case len(b) == ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(b), nil
	case len(b) == ed25519.PrivateKeySize:
		return ed25519.PrivateKey(b), nil
	}
	return nil, errors.New("UPDATE_SIGNING_KEY is not an Ed25519 key (empty?)")
}
