package multipart

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/access/policy"
	"github.com/PRIEYAN/ButterDB/internal/access/sigv4"
	"github.com/PRIEYAN/ButterDB/internal/domain/access"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/ingest/multipartstage"
	"github.com/PRIEYAN/ButterDB/internal/readpath/objectreader"
)

type Stage interface {
	Create(ctx context.Context, req multipartstage.CreateRequest) (multipartstage.Upload, error)
	PutPart(ctx context.Context, uploadID string, partNumber int, size int64, body io.Reader) (multipartstage.Part, error)
	Complete(ctx context.Context, uploadID string, parts []multipartstage.CompletedPart, cond multipartstage.Conditions) (object.ObjectMeta, error)
	Abort(ctx context.Context, uploadID string) error
	ListParts(ctx context.Context, uploadID string, marker int, max int) (multipartstage.PartPage, error)
	ListUploads(ctx context.Context, bucketChannelID int64, q multipartstage.UploadQuery) (multipartstage.UploadPage, error)
}

type PayloadOpener interface {
	Body(r *http.Request, a sigv4.Auth) (*sigv4.PayloadReader, error)
}

type BucketAuthorizer interface {
	Authorize(ctx context.Context, p policy.Principal, b bucket.BucketName, a access.Action) (bucket.Info, error)
}

type UploadRecord struct {
	ID                   string
	BucketChannelID      int64
	Bucket               string
	Key                  string
	InitiatorAccessKeyID string
	InitiatedAt          time.Time
	State                string
	Encrypted            bool
	ChecksumAlgorithm    string
	ChecksumType         string
}

type UploadLookup interface {
	LookupUpload(ctx context.Context, uploadID string) (UploadRecord, error)
}

type SourceReader interface {
	Open(ctx context.Context, bucketName, key string) (object.ObjectMeta, objectreader.Opened, error)
}
