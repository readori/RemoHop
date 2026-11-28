package config

import (
	"path/filepath"
	"strings"
)

// inProfile reports whether dir is inside profile, the user's profile
// directory, comparing cleaned absolute paths without regard to case (as
// Windows does). An empty profile contains nothing.
func inProfile(dir, profile string) bool {
	if profile == "" || dir == "" {
		return false
	}
	d, err1 := filepath.Abs(dir)
	p, err2 := filepath.Abs(profile)
	if err1 != nil || err2 != nil {
		return false
	}
	d, p = strings.ToLower(filepath.Clean(d)), strings.ToLower(filepath.Clean(p))
	return d == p || strings.HasPrefix(d, strings.TrimSuffix(p, string(filepath.Separator))+string(filepath.Separator))
}
