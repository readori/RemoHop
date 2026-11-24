package input

import (
	"context"

	"github.com/readori/RemoHop/internal/inject"
	"github.com/readori/RemoHop/internal/svc/screenshot"
)

// openBackend uses the screenshot service's monitor list, whose virtual
// desktop coordinates are the ones SendInput addresses.
func openBackend(ctx context.Context) (*backend, error) {
	ds, err := screenshot.LocalDisplays(ctx)
	if err != nil {
		return nil, err
	}
	inj, err := inject.Open(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &backend{inj: inj, displays: ds}, nil
}
