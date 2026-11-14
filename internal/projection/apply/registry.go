package apply

import (
	"fmt"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/projection"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

type KeyGeneration interface {
	Advance(bucketChannelID int64)
}

type Dependencies struct {
	Generation KeyGeneration
}

type Registry struct {
	appliers map[wire.OpType]projection.Applier
}

func NewRegistry(deps Dependencies) (*Registry, error) {
	if deps.Generation == nil {
		return nil, fmt.Errorf("apply: %w: key generation", errs.ErrMissingDependency)
	}
	return &Registry{appliers: map[wire.OpType]projection.Applier{
		wire.OpBody:         bodyApplier{},
		wire.OpPut:          putApplier{},
		wire.OpDel:          delApplier{},
		wire.OpBucketCreate: bucketCreateApplier{},
		wire.OpBucketConfig: bucketConfigApplier{},
		wire.OpBucketDelete: bucketDeleteApplier{generation: deps.Generation},
	}}, nil
}

func (r *Registry) Lookup(t wire.OpType) (projection.Applier, bool) {
	applier, ok := r.appliers[t]
	return applier, ok
}
