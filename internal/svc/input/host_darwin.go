package input

import (
	"context"

	"github.com/readori/RemoHop/internal/inject"
)

// openBackend checks the Accessibility permission (inject.Open fails with
// instructions without it) and lists the displays with their point
// geometry, so pixel coordinates from a screenshot land on the same spot
// on Retina and non-Retina displays alike.
func openBackend(ctx context.Context) (*backend, error) {
	inj, err := inject.Open(ctx, nil)
	if err != nil {
		return nil, err
	}
	geom, err := inject.MacDisplays()
	if err != nil {
		inj.Close()
		return nil, err
	}
	return &backend{inj: inj, displays: macDisplays(geom), space: hostSpace{geom: geom}}, nil
}
