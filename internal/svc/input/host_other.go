//go:build !linux && !windows && !darwin

package input

import (
	"context"

	"github.com/readori/RemoHop/internal/inject"
)

// openBackend fails with the injector's "not supported" error (Open
// always fails on these systems).
func openBackend(ctx context.Context) (*backend, error) {
	_, err := inject.Open(ctx, nil)
	return nil, err
}
