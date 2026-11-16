package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/internal/adminapi/handlers"
	"github.com/PRIEYAN/ButterDB/internal/adminapi/session"
	"github.com/PRIEYAN/ButterDB/internal/adminapi/setup"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/identity"
	"github.com/PRIEYAN/ButterDB/internal/s3api/router"
	"github.com/PRIEYAN/ButterDB/internal/storage/objectrepo"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
)

var (
	_ handlers.BucketService = adminBuckets{}
	_ handlers.ObjectLister  = adminObjectLister{}
	_ handlers.ObjectWriter  = adminObjectWriter{}
)

type readinessSource interface {
	State() router.ServerState
}

type Backend struct {
	Sync      *Sync
	Write     *Write
	Read      *Read
	Database  *sqlitedb.DB
	Readiness readinessSource
}

type BackendSlot struct {
	current atomic.Pointer[Backend]
}

func (s *BackendSlot) Publish(backend *Backend) {
	s.current.Store(backend)
}

func (s *BackendSlot) Clear() {
	s.current.Store(nil)
}

func (s *BackendSlot) Current() *Backend {
	return s.current.Load()
}

func (rt *Runtime) Backend() *Backend {
	return &Backend{Sync: rt.Sync, Write: rt.Write, Read: rt.Read, Database: rt.database, Readiness: rt.S3.Readiness}
}

type backendGate struct {
	slot  *BackendSlot
	relay setup.LoginRelay
}

func (g backendGate) backend(ctx context.Context) (*Backend, error) {
	backend := g.slot.Current()
	if backend == nil {
		return nil, g.unavailable(ctx)
	}
	if backend.Readiness != nil && backend.Readiness.State() == router.StateStarting {
		return nil, fmt.Errorf("core: admin: initial sync is still running: %w", handlers.ErrServerNotReady)
	}
	return backend, nil
}

func (g backendGate) unavailable(ctx context.Context) error {
	snap, err := g.relay.Status(ctx)
	if err == nil && snap.State != identity.StateLinked {
		return fmt.Errorf("core: admin: %w", handlers.ErrTelegramNotLinked)
	}
	return fmt.Errorf("core: admin: storage runtime is not serving: %w", handlers.ErrServerNotReady)
}

func (g backendGate) bucketInfo(ctx context.Context, backend *Backend, name bucket.BucketName) (bucket.Info, error) {
	info, found, err := backend.Read.Buckets.Lookup(ctx, name)
	if err != nil {
		return bucket.Info{}, fmt.Errorf("core: admin: look up bucket %q: %w", name, err)
	}
	if !found {
		return bucket.Info{}, fmt.Errorf("core: admin: bucket %q: %w", name, errs.ErrNoSuchBucket)
	}
	return info, nil
}

type adminBuckets struct {
	gate backendGate
}

func (a adminBuckets) List(ctx context.Context) ([]bucket.Info, error) {
	backend, err := a.gate.backend(ctx)
	if err != nil {
		return nil, err
	}
	return backend.Sync.Buckets.List(ctx)
}

func (a adminBuckets) Create(ctx context.Context, spec bucket.Spec) (bucket.Info, error) {
	backend, err := a.gate.backend(ctx)
	if err != nil {
		return bucket.Info{}, err
	}
	return backend.Sync.Buckets.Create(ctx, spec)
}

func (a adminBuckets) Get(ctx context.Context, name bucket.BucketName) (bucket.Info, error) {
	backend, err := a.gate.backend(ctx)
	if err != nil {
		return bucket.Info{}, err
	}
	return backend.Sync.Buckets.Get(ctx, name)
}

func (a adminBuckets) Update(ctx context.Context, name bucket.BucketName, patch bucket.Patch) (bucket.Info, error) {
	backend, err := a.gate.backend(ctx)
	if err != nil {
		return bucket.Info{}, err
	}
	return backend.Sync.Buckets.Update(ctx, name, patch)
}

func (a adminBuckets) Delete(ctx context.Context, name bucket.BucketName) error {
	backend, err := a.gate.backend(ctx)
	if err != nil {
		return err
	}
	return backend.Sync.Buckets.Delete(ctx, name)
}

type adminObjectLister struct {
	gate backendGate
}

func (l adminObjectLister) List(ctx context.Context, name bucket.BucketName, query object.ListQuery) (object.ListPage, error) {
	backend, err := l.gate.backend(ctx)
	if err != nil {
		return object.ListPage{}, err
	}
	info, err := l.gate.bucketInfo(ctx, backend, name)
	if err != nil {
		return object.ListPage{}, err
	}
	page, err := backend.Read.Live.List(ctx, info.ChannelID, query)
	if errors.Is(err, objectrepo.ErrInvalidContinuationToken) {
		return object.ListPage{}, &handlers.BadRequestError{Field: adminv1.QueryCursor}
	}
	return page, err
}

func (l adminObjectLister) Meta(ctx context.Context, name bucket.BucketName, key object.Key) (object.ObjectMeta, error) {
	backend, err := l.gate.backend(ctx)
	if err != nil {
		return object.ObjectMeta{}, err
	}
	info, err := l.gate.bucketInfo(ctx, backend, name)
	if err != nil {
		return object.ObjectMeta{}, err
	}
	return backend.Read.Live.Get(ctx, info.ChannelID, string(key))
}

type adminObjectWriter struct {
	gate backendGate
}

func (w adminObjectWriter) Put(ctx context.Context, req object.PutRequest, body io.Reader) (object.ObjectMeta, error) {
	backend, err := w.gate.backend(ctx)
	if err != nil {
		return object.ObjectMeta{}, err
	}
	return backend.Write.Put.Put(ctx, req, body)
}

func (w adminObjectWriter) Delete(ctx context.Context, name bucket.BucketName, key object.Key) error {
	backend, err := w.gate.backend(ctx)
	if err != nil {
		return err
	}
	device, ok := session.DeviceFromContext(ctx)
	if !ok {
		return fmt.Errorf("core: admin: delete without a device: %w", errs.ErrAccessDenied)
	}
	principal, err := PrincipalForCaller(deviceScopePrefix+device.DeviceID, handlers.RequestIDFromContext(ctx))
	if err != nil {
		return err
	}
	return backend.Write.Remove.Delete(WithPrincipal(ctx, principal), name, key)
}
