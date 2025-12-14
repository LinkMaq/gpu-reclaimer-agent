package sampling

import "context"

type Sampler interface {
	Sample(ctx context.Context) (Snapshot, error)
	Close() error
	Name() string
}
