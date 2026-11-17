package app

import (
	"context"
	"fmt"
	"io"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
)

type sessionAPI interface {
	Audit(ctx context.Context, q adminv1.AuditQuery) (adminv1.AuditPage, error)
	CancelTelegramLogin(ctx context.Context) (adminv1.TelegramStatus, error)
	CreateAccessKey(ctx context.Context, req adminv1.CreateAccessKeyRequest) (adminv1.AccessKeyCreated, error)
	CreateBucket(ctx context.Context, req adminv1.CreateBucketRequest) (adminv1.Bucket, error)
	DeleteBucket(ctx context.Context, name string) error
	DeleteObject(ctx context.Context, bucket, key string) error
	DeleteObjects(ctx context.Context, bucket string, req adminv1.DeleteObjectsRequest) (adminv1.DeleteObjectsResult, error)
	DeleteTelegramCredentials(ctx context.Context) (adminv1.TelegramStatus, error)
	GetBucket(ctx context.Context, name string) (adminv1.Bucket, error)
	GetObject(ctx context.Context, bucket, key string, opts adminv1.GetObjectOptions) (adminv1.ObjectStream, error)
	HeadObject(ctx context.Context, bucket, key string) (adminv1.ObjectMeta, error)
	ListAccessKeys(ctx context.Context, includeRevoked bool) (adminv1.AccessKeyList, error)
	ListBuckets(ctx context.Context) (adminv1.BucketList, error)
	ListObjects(ctx context.Context, bucket string, opts adminv1.ListObjectsOptions) (adminv1.ObjectList, error)
	ObjectMeta(ctx context.Context, bucket, key string) (adminv1.ObjectMeta, error)
	Presign(ctx context.Context, bucket string, req adminv1.PresignRequest) (adminv1.PresignedURL, error)
	PutObject(ctx context.Context, bucket, key string, body io.Reader, size int64, opts adminv1.PutObjectOptions) (adminv1.ObjectMeta, error)
	RevokeAccessKey(ctx context.Context, id string) (adminv1.AccessKey, error)
	SaveTelegramCredentials(ctx context.Context, req adminv1.TelegramCredentialsRequest) (adminv1.TelegramStatus, error)
	StartTelegramLogin(ctx context.Context, req adminv1.TelegramLoginStartRequest) (adminv1.TelegramStatus, error)
	Status(ctx context.Context) (adminv1.Status, error)
	SubmitTelegramCode(ctx context.Context, req adminv1.TelegramCodeRequest) (adminv1.TelegramStatus, error)
	SubmitTelegramPassword(ctx context.Context, req adminv1.TelegramPasswordRequest) (adminv1.TelegramStatus, error)
	TelegramStatus(ctx context.Context) (adminv1.TelegramStatus, error)
	UpdateAccessKey(ctx context.Context, id string, req adminv1.UpdateAccessKeyRequest) (adminv1.AccessKey, error)
	UpdateBucket(ctx context.Context, name string, req adminv1.UpdateBucketRequest) (adminv1.Bucket, error)
	Usage(ctx context.Context) (adminv1.Usage, error)
}

type guardedClient struct {
	upstream       sessionAPI
	onSessionError func(err error)
}

func newGuardedClient(upstream sessionAPI, onSessionError func(err error)) (*guardedClient, error) {
	if upstream == nil {
		return nil, fmt.Errorf("app: %w: upstream client", ErrMissingDependency)
	}
	if onSessionError == nil {
		return nil, fmt.Errorf("app: %w: session error handler", ErrMissingDependency)
	}
	return &guardedClient{upstream: upstream, onSessionError: onSessionError}, nil
}

func (g *guardedClient) note(err error) error {
	if isSessionError(err) {
		g.onSessionError(err)
	}
	return err
}

func isSessionError(err error) bool {
	return adminv1.IsCode(err, adminv1.CodeUnauthenticated) || adminv1.IsCode(err, adminv1.CodeTelegramNotLinked)
}

func (g *guardedClient) TelegramStatus(ctx context.Context) (adminv1.TelegramStatus, error) {
	v, err := g.upstream.TelegramStatus(ctx)
	return v, g.note(err)
}

func (g *guardedClient) SaveTelegramCredentials(ctx context.Context, req adminv1.TelegramCredentialsRequest) (adminv1.TelegramStatus, error) {
	v, err := g.upstream.SaveTelegramCredentials(ctx, req)
	return v, g.note(err)
}

func (g *guardedClient) DeleteTelegramCredentials(ctx context.Context) (adminv1.TelegramStatus, error) {
	v, err := g.upstream.DeleteTelegramCredentials(ctx)
	return v, g.note(err)
}

func (g *guardedClient) StartTelegramLogin(ctx context.Context, req adminv1.TelegramLoginStartRequest) (adminv1.TelegramStatus, error) {
	v, err := g.upstream.StartTelegramLogin(ctx, req)
	return v, g.note(err)
}

func (g *guardedClient) SubmitTelegramCode(ctx context.Context, req adminv1.TelegramCodeRequest) (adminv1.TelegramStatus, error) {
	v, err := g.upstream.SubmitTelegramCode(ctx, req)
	return v, g.note(err)
}

func (g *guardedClient) SubmitTelegramPassword(ctx context.Context, req adminv1.TelegramPasswordRequest) (adminv1.TelegramStatus, error) {
	v, err := g.upstream.SubmitTelegramPassword(ctx, req)
	return v, g.note(err)
}

func (g *guardedClient) CancelTelegramLogin(ctx context.Context) (adminv1.TelegramStatus, error) {
	v, err := g.upstream.CancelTelegramLogin(ctx)
	return v, g.note(err)
}

func (g *guardedClient) Status(ctx context.Context) (adminv1.Status, error) {
	v, err := g.upstream.Status(ctx)
	return v, g.note(err)
}

func (g *guardedClient) Usage(ctx context.Context) (adminv1.Usage, error) {
	v, err := g.upstream.Usage(ctx)
	return v, g.note(err)
}

func (g *guardedClient) ListBuckets(ctx context.Context) (adminv1.BucketList, error) {
	v, err := g.upstream.ListBuckets(ctx)
	return v, g.note(err)
}

func (g *guardedClient) CreateBucket(ctx context.Context, req adminv1.CreateBucketRequest) (adminv1.Bucket, error) {
	v, err := g.upstream.CreateBucket(ctx, req)
	return v, g.note(err)
}

func (g *guardedClient) GetBucket(ctx context.Context, name string) (adminv1.Bucket, error) {
	v, err := g.upstream.GetBucket(ctx, name)
	return v, g.note(err)
}

func (g *guardedClient) UpdateBucket(ctx context.Context, name string, req adminv1.UpdateBucketRequest) (adminv1.Bucket, error) {
	v, err := g.upstream.UpdateBucket(ctx, name, req)
	return v, g.note(err)
}

func (g *guardedClient) ListAccessKeys(ctx context.Context, includeRevoked bool) (adminv1.AccessKeyList, error) {
	v, err := g.upstream.ListAccessKeys(ctx, includeRevoked)
	return v, g.note(err)
}

func (g *guardedClient) CreateAccessKey(ctx context.Context, req adminv1.CreateAccessKeyRequest) (adminv1.AccessKeyCreated, error) {
	v, err := g.upstream.CreateAccessKey(ctx, req)
	return v, g.note(err)
}

func (g *guardedClient) UpdateAccessKey(ctx context.Context, id string, req adminv1.UpdateAccessKeyRequest) (adminv1.AccessKey, error) {
	v, err := g.upstream.UpdateAccessKey(ctx, id, req)
	return v, g.note(err)
}

func (g *guardedClient) RevokeAccessKey(ctx context.Context, id string) (adminv1.AccessKey, error) {
	v, err := g.upstream.RevokeAccessKey(ctx, id)
	return v, g.note(err)
}

func (g *guardedClient) Audit(ctx context.Context, q adminv1.AuditQuery) (adminv1.AuditPage, error) {
	v, err := g.upstream.Audit(ctx, q)
	return v, g.note(err)
}

func (g *guardedClient) ListObjects(ctx context.Context, bucket string, opts adminv1.ListObjectsOptions) (adminv1.ObjectList, error) {
	v, err := g.upstream.ListObjects(ctx, bucket, opts)
	return v, g.note(err)
}

func (g *guardedClient) HeadObject(ctx context.Context, bucket, key string) (adminv1.ObjectMeta, error) {
	v, err := g.upstream.HeadObject(ctx, bucket, key)
	return v, g.note(err)
}

func (g *guardedClient) ObjectMeta(ctx context.Context, bucket, key string) (adminv1.ObjectMeta, error) {
	v, err := g.upstream.ObjectMeta(ctx, bucket, key)
	return v, g.note(err)
}

func (g *guardedClient) GetObject(ctx context.Context, bucket, key string, opts adminv1.GetObjectOptions) (adminv1.ObjectStream, error) {
	v, err := g.upstream.GetObject(ctx, bucket, key, opts)
	return v, g.note(err)
}

func (g *guardedClient) PutObject(ctx context.Context, bucket, key string, body io.Reader, size int64, opts adminv1.PutObjectOptions) (adminv1.ObjectMeta, error) {
	v, err := g.upstream.PutObject(ctx, bucket, key, body, size, opts)
	return v, g.note(err)
}

func (g *guardedClient) DeleteObjects(ctx context.Context, bucket string, req adminv1.DeleteObjectsRequest) (adminv1.DeleteObjectsResult, error) {
	v, err := g.upstream.DeleteObjects(ctx, bucket, req)
	return v, g.note(err)
}

func (g *guardedClient) Presign(ctx context.Context, bucket string, req adminv1.PresignRequest) (adminv1.PresignedURL, error) {
	v, err := g.upstream.Presign(ctx, bucket, req)
	return v, g.note(err)
}

func (g *guardedClient) DeleteBucket(ctx context.Context, name string) error {
	return g.note(g.upstream.DeleteBucket(ctx, name))
}

func (g *guardedClient) DeleteObject(ctx context.Context, bucket, key string) error {
	return g.note(g.upstream.DeleteObject(ctx, bucket, key))
}

var _ sessionAPI = (*adminv1.Client)(nil)

var _ sessionAPI = (*guardedClient)(nil)
