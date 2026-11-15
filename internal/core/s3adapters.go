package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/access/sigv4"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/s3api/listing"
	"github.com/PRIEYAN/ButterDB/internal/s3api/multipart"
	"github.com/PRIEYAN/ButterDB/internal/s3api/router"
	"github.com/PRIEYAN/ButterDB/internal/storage/bucketrepo"
	"github.com/PRIEYAN/ButterDB/internal/storage/jobrepo"
	"github.com/PRIEYAN/ButterDB/internal/storage/objectrepo"
)

type s3PrincipalAttacher struct{}

func (s3PrincipalAttacher) WithPrincipal(ctx context.Context, accessKeyID, requestID string) (context.Context, error) {
	principal, err := PrincipalForCaller(accessKeyID, requestID)
	if err != nil {
		return ctx, err
	}
	return WithPrincipal(ctx, principal), nil
}

type s3BucketReader struct {
	buckets *bucketrepo.Repository
}

func (r s3BucketReader) Get(ctx context.Context, name bucket.BucketName) (bucket.Info, error) {
	info, found, err := r.buckets.Lookup(ctx, name)
	if err != nil {
		return bucket.Info{}, fmt.Errorf("core: s3: look up bucket: %w", err)
	}
	if !found {
		return bucket.Info{}, fmt.Errorf("core: s3: bucket %q: %w", name, errs.ErrNoSuchBucket)
	}
	return info, nil
}

type s3BodyOpener struct {
	verifier *sigv4.Verifier
}

func (o s3BodyOpener) OpenBody(r *http.Request, a sigv4.Auth) (io.ReadCloser, error) {
	body, err := o.verifier.Body(r, a)
	if err != nil {
		return nil, err
	}
	return body, nil
}

type s3ObjectLister struct {
	objects *objectrepo.Repository
}

func (l s3ObjectLister) List(ctx context.Context, channelID int64, query object.ListQuery) (object.ListPage, error) {
	page, err := l.objects.List(ctx, channelID, query)
	if errors.Is(err, objectrepo.ErrInvalidContinuationToken) {
		return object.ListPage{}, fmt.Errorf("core: s3: list objects: %w", listing.ErrInvalidContinuationToken)
	}
	return page, err
}

type s3UploadLookup struct {
	index *jobrepo.MultipartIndex
}

func (l s3UploadLookup) LookupUpload(ctx context.Context, uploadID string) (multipart.UploadRecord, error) {
	row, err := l.index.GetUpload(ctx, uploadID)
	if err != nil {
		return multipart.UploadRecord{}, fmt.Errorf("core: s3: look up upload: %w", err)
	}
	return multipart.UploadRecord{
		ID:                   row.UploadID,
		BucketChannelID:      row.BucketChannelID,
		Bucket:               row.BucketName,
		Key:                  row.ObjectKey,
		InitiatorAccessKeyID: row.InitiatorAccessKeyID,
		InitiatedAt:          time.UnixMilli(row.InitiatedAt),
		State:                string(row.State),
		Encrypted:            row.Encrypted,
		ChecksumAlgorithm:    row.ChecksumAlgorithm,
		ChecksumType:         row.ChecksumType,
	}, nil
}

type s3Readiness struct {
	state atomic.Int32
}

func newS3Readiness() *s3Readiness {
	readiness := &s3Readiness{}
	readiness.set(router.StateStarting)
	return readiness
}

func (r *s3Readiness) State() router.ServerState {
	return router.ServerState(r.state.Load())
}

func (r *s3Readiness) set(state router.ServerState) {
	r.state.Store(int32(state))
}
