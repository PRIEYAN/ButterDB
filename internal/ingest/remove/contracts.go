package remove

import (
	"context"

	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
)

type Service interface {
	Delete(ctx context.Context, name bucket.BucketName, key object.Key) error
}
