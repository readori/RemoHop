//go:build !darwin

package update

// VerifyBundle only applies to macOS app bundles.
func VerifyBundle(string) error { return nil }
