package objectreader

import (
	"context"

	"github.com/PRIEYAN/ButterDB/internal/domain/object"
)

type Opened interface {
	ReadAt(ctx context.Context, dst []byte, off int64) (int, error)
	Close() error
}

type Service interface {
	Open(ctx context.Context, meta object.ObjectMeta) (Opened, error)
}

type Source interface {
	ReadDocumentRange(ctx context.Context, meta object.ObjectMeta, offset int64, dst []byte) (int, error)
}
