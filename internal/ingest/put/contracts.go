package put

import (
	"context"
	"io"

	"github.com/PRIEYAN/ButterDB/internal/domain/object"
)

type Service interface {
	Put(ctx context.Context, req object.PutRequest, body io.Reader) (object.ObjectMeta, error)
}
