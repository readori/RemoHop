package auth

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"unicode"
)

const (
	// passwordAlphabet avoids the characters people confuse when reading a
	// password aloud or off a screen (0/o, 1/l/i).
	passwordAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	// passwordLen gives 8*log2(31) ≈ 39.6 bits. That is far too little for
	// an offline attack and exactly why the handshake is a PAKE: guesses
	// can only be made online, one per connection, and the host's Limiter
	// holds a distributed attacker to roughly one guess every five seconds
	// in the long run. At that rate a year of guessing succeeds with
	// probability about 7e-6. Shorter (6 characters, ≈30 bits) would put a
	// year-long attack in the percent range, which is too much for a
	// credential that grants a shell.
	passwordLen = 8
	// MinPasswordLen is the shortest user-chosen password accepted. A fixed
	// password does not rotate, so it must not be weaker than a generated
	// one in length.
	MinPasswordLen = 8
)

// GeneratePassword returns a fresh random temporary password in canonical
// form (no separators).
func GeneratePassword() (string, error) {
	n := big.NewInt(int64(len(passwordAlphabet)))
	out := make([]byte, passwordLen)
	for i := range out {
		v, err := rand.Int(rand.Reader, n)
		if err != nil {
			return "", err
		}
		out[i] = passwordAlphabet[v.Int64()]
	}
	return string(out), nil
}

// FormatPassword groups a generated password for display ("abcd-efgh").
// Passwords of any other shape are returned unchanged.
func FormatPassword(p string) string {
	if len(p) != passwordLen {
		return p
	}
	for i := 0; i < len(p); i++ {
		if strings.IndexByte(passwordAlphabet, p[i]) < 0 {
			return p
		}
	}
	return p[:passwordLen/2] + "-" + p[passwordLen/2:]
}

// NormalizePassword maps what a user typed to the canonical form both ends
// feed to the PAKE: whitespace and dashes are dropped, so "abcd-efgh",
// "abcd efgh" and "abcdefgh" are the same password. Both sides apply it to
// every password, generated or user-chosen, so they always agree.
func NormalizePassword(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '-' || unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}

// CheckPassword validates a user-chosen host password (already normalised).
func CheckPassword(p string) error {
	if len(p) < MinPasswordLen {
		return fmt.Errorf("password too short: need at least %d characters (not counting spaces and dashes)", MinPasswordLen)
	}
	if len(p) > 256 {
		return errors.New("password too long")
	}
	return nil
}
