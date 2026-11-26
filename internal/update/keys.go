package update

// TrustedKeys are the Ed25519 public keys (base64) whose signatures on a
// release's SHA256SUMS this build accepts for one-click upgrades (sign.go).
//
//   - The first is the release workflow's key; its private half is only
//     the repository secret UPDATE_SIGNING_KEY.
//   - The second, when set, is a spare kept offline by the project's owner:
//     if the first is ever lost or leaked, releases are signed with the
//     spare, and a release that drops the old key goes out signed by it.
//
// Empty: one-click upgrades are off, and the app offers the download page.
// `go run ./scripts/updsign keygen` makes a key pair.
var TrustedKeys = []string{
	"EhMAtz32qkaC5xnCXOo6SLO2J/fH6WzxkIV3wvZuUyQ=", // release (secret UPDATE_SIGNING_KEY), made 2026-11-26
	"fdzFU7FhCrIrtD3sTKmcEP4Y1TioJ6LKurOp5fzetSo=", // spare, offline with the owner
}
