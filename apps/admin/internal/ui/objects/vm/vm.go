package vm

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

type Client interface {
	ListBuckets(ctx context.Context) (adminv1.BucketList, error)
	ListObjects(ctx context.Context, bucket string, opts adminv1.ListObjectsOptions) (adminv1.ObjectList, error)
	HeadObject(ctx context.Context, bucket, key string) (adminv1.ObjectMeta, error)
	ObjectMeta(ctx context.Context, bucket, key string) (adminv1.ObjectMeta, error)
	GetObject(ctx context.Context, bucket, key string, opts adminv1.GetObjectOptions) (adminv1.ObjectStream, error)
	PutObject(ctx context.Context, bucket, key string, body io.Reader, size int64, opts adminv1.PutObjectOptions) (adminv1.ObjectMeta, error)
	DeleteObject(ctx context.Context, bucket, key string) error
	DeleteObjects(ctx context.Context, bucket string, req adminv1.DeleteObjectsRequest) (adminv1.DeleteObjectsResult, error)
	Presign(ctx context.Context, bucket string, req adminv1.PresignRequest) (adminv1.PresignedURL, error)
	ListAccessKeys(ctx context.Context, includeRevoked bool) (adminv1.AccessKeyList, error)
	Status(ctx context.Context) (adminv1.Status, error)
}

const (
	ListPageSize             = 200
	Delimiter                = "/"
	DefaultUploadConcurrency = 2
	MaxUploadConcurrency     = 4
	progressInterval         = 100 * time.Millisecond
	idempotencyKeyBytes      = 16
)

type Option func(*ViewModel)

func WithFileSystem(fs FileSystem) Option {
	return func(v *ViewModel) {
		if fs != nil {
			v.fs = fs
		}
	}
}

func WithRandom(r io.Reader) Option {
	return func(v *ViewModel) {
		if r != nil {
			v.random = r
		}
	}
}

func WithSleep(fn func(ctx context.Context, d time.Duration) error) Option {
	return func(v *ViewModel) {
		if fn != nil {
			v.sleep = fn
		}
	}
}

func WithUploadConcurrency(n int) Option {
	return func(v *ViewModel) {
		v.slots = make(chan struct{}, min(max(n, 1), MaxUploadConcurrency))
	}
}

type ViewModel struct {
	client Client
	now    func() time.Time
	fs     FileSystem
	random io.Reader
	sleep  func(ctx context.Context, d time.Duration) error
	slots  chan struct{}

	mu          sync.Mutex
	state       *model.Observable[State]
	generation  int
	focusGen    int
	loading     bool
	loadingMore bool
	problem     *model.Problem
	buckets     []string
	bucket      string
	prefix      string
	filter      string
	folders     []string
	objects     []adminv1.ObjectSummary
	cursor      string
	selObjects  map[string]struct{}
	selFolders  map[string]struct{}
	limits      adminv1.Limits
	limitsKnown bool
	details     Details
	preview     Preview

	tmu         sync.Mutex
	transfers   *model.Observable[TransferList]
	jobs        []*job
	nextJobID   int
	pausedUntil time.Time
}

func New(c Client, now func() time.Time, opts ...Option) *ViewModel {
	if c == nil {
		panic("objects/vm: nil client")
	}
	if now == nil {
		panic("objects/vm: nil clock")
	}
	v := &ViewModel{
		client:     c,
		now:        now,
		fs:         osFileSystem{},
		random:     rand.Reader,
		sleep:      sleepContext,
		slots:      make(chan struct{}, DefaultUploadConcurrency),
		selObjects: make(map[string]struct{}),
		selFolders: make(map[string]struct{}),
		state:      model.NewObservable(State{}),
		transfers:  model.NewObservable(TransferList{}),
	}
	for _, opt := range opts {
		opt(v)
	}
	return v
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (v *ViewModel) State() *model.Observable[State] {
	return v.state
}

func (v *ViewModel) Transfers() *model.Observable[TransferList] {
	return v.transfers
}

func (v *ViewModel) problemFor(err error) *model.Problem {
	p := model.Describe(err, v.now())
	if p.Code == "" && p.Title == "" {
		return nil
	}
	return &p
}

func syntheticAPIError(status int, code adminv1.Code, details map[string]any) error {
	return &adminv1.APIError{Status: status, Body: adminv1.ErrorBody{Code: code, Details: details}}
}

func validationProblem(field, reason string) *model.Problem {
	err := syntheticAPIError(422, adminv1.CodeValidationFailed, map[string]any{"field": field, "reason": reason})
	p := model.Describe(err, time.Time{})
	return &p
}

func isAbsent(err error) bool {
	if adminv1.IsCode(err, adminv1.CodeObjectNotFound) || adminv1.IsCode(err, adminv1.CodeNotFound) {
		return true
	}
	var apiErr *adminv1.APIError
	return errors.As(err, &apiErr) && apiErr != nil && apiErr.Status == 404
}

func (v *ViewModel) ensureLimits(ctx context.Context) (adminv1.Limits, error) {
	v.mu.Lock()
	if v.limitsKnown {
		limits := v.limits
		v.mu.Unlock()
		return limits, nil
	}
	v.mu.Unlock()
	status, err := v.client.Status(ctx)
	if err != nil {
		return adminv1.Limits{}, fmt.Errorf("objects: status: %w", err)
	}
	v.mutate(func() {
		v.limits = status.Limits
		v.limitsKnown = true
	})
	return status.Limits, nil
}

func (v *ViewModel) refreshLimits(ctx context.Context) {
	status, err := v.client.Status(ctx)
	if err != nil {
		return
	}
	v.mutate(func() {
		v.limits = status.Limits
		v.limitsKnown = true
	})
}

func (v *ViewModel) newIdempotencyKey() (string, error) {
	buf := make([]byte, idempotencyKeyBytes)
	if _, err := io.ReadFull(v.random, buf); err != nil {
		return "", fmt.Errorf("objects: idempotency key: %w", err)
	}
	return fmt.Sprintf("%x", buf), nil
}
