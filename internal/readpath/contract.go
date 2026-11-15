package readpath

import (
	"context"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

type DocumentRangeReader interface {
	ReadDocumentRange(ctx context.Context, ref tgclient.DocumentRef, offset int64, dst []byte) (int, error)
}

type StoredReader interface {
	ReadStoredAt(ctx context.Context, ref tgclient.DocumentRef, dst []byte, off int64) (int, error)
}
