package desktop

import (
	"context"
	"errors"

	"github.com/readori/RemoHop/internal/inject"
)

func newInjector(spec captureSpec) (injector, error) {
	// Only the primary monitor's size is known (see discoverCapture), and
	// it sits at the virtual desktop's origin.
	if spec.width == 0 {
		return nil, errors.New("input is only supported on the primary display on Windows hosts")
	}
	inj, err := inject.Open(context.Background(), nil)
	if err != nil {
		return nil, err
	}
	return &hostInjector{inj: inj, spec: spec}, nil
}
