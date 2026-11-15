package multipartstage

import (
	"context"
	"io"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
	"github.com/PRIEYAN/ButterDB/internal/storage/jobrepo"
)

type CreateRequest struct {
	Bucket            bucket.BucketName
	BucketChannelID   int64
	Key               object.Key
	ContentType       string
	UserMeta          map[string]string
	SystemMeta        map[string]string
	Encrypted         bool
	ChecksumAlgorithm string
	AccessKeyID       string
}

type Upload struct {
	ID          string
	Bucket      bucket.BucketName
	Key         object.Key
	InitiatedAt time.Time
	State       string
}

type Part struct {
	Number   int
	Size     int64
	ETag     string
	Checksum string
	StagedAt time.Time
}

type CompletedPart struct {
	Number   int
	ETag     string
	Checksum string
}

type Conditions struct {
	IfMatch        string
	IfNoneMatchAny bool
}

type PartPage struct {
	Parts      []Part
	NextMarker int
	Truncated  bool
}

type UploadQuery = jobrepo.MultipartUploadQuery

type UploadPage struct {
	Uploads            []Upload
	CommonPrefixes     []string
	NextKeyMarker      string
	NextUploadIDMarker string
	Truncated          bool
}

type Index interface {
	CreateUpload(ctx context.Context, u jobrepo.MultipartUpload) error
	GetUpload(ctx context.Context, id string) (jobrepo.MultipartUpload, error)
	PutPart(ctx context.Context, p jobrepo.MultipartPart) error
	ListParts(ctx context.Context, id string, marker int, max int) ([]jobrepo.MultipartPart, bool, error)
	ListUploads(ctx context.Context, bucketChannelID int64, q UploadQuery) ([]jobrepo.MultipartUpload, bool, error)
	Transition(ctx context.Context, id string, from, to string, patch jobrepo.MultipartPatch) error
	Delete(ctx context.Context, id string) error
	IdleBefore(ctx context.Context, t time.Time, limit int) ([]string, error)
}

type Putter interface {
	PutMultipart(ctx context.Context, req object.PutRequest, body io.Reader, uploadID, jobID string) (object.ObjectMeta, error)
}

type Dependencies struct {
	Index  Index
	Putter Putter
	Dir    string
	Clock  clock.Clock
	Random io.Reader
	FS     fsx.FS
}
