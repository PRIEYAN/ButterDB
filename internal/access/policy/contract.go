package policy

import (
	"context"

	"github.com/PRIEYAN/ButterDB/internal/domain/access"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
)

type Principal struct {
	Anonymous bool
	Key       access.KeyInfo
}

type BucketSource interface {
	Lookup(ctx context.Context, name bucket.BucketName) (bucket.Info, bool, error)
}
