package bucketops

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/access/policy"
	"github.com/PRIEYAN/ButterDB/internal/access/sigv4"
	"github.com/PRIEYAN/ButterDB/internal/domain/access"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/s3api/router"
)

const testOwnerID = "5d0a000000000000000000000000000000000000000000000000000000000064"

type fakeBuckets struct {
	infos map[bucket.BucketName]bucket.Info
}

func (f fakeBuckets) Get(_ context.Context, name bucket.BucketName) (bucket.Info, error) {
	info, ok := f.infos[name]
	if !ok {
		return bucket.Info{}, errs.ErrNoSuchBucket
	}
	return info, nil
}

type fakeEvaluator struct {
	infos map[bucket.BucketName]bucket.Info
	err   error
	calls []access.Action
}

func (f *fakeEvaluator) Authorize(_ context.Context, p policy.Principal, b bucket.BucketName, a access.Action) (bucket.Info, error) {
	f.calls = append(f.calls, a)
	if f.err != nil {
		return bucket.Info{}, f.err
	}
	if !p.Key.Scope.CoversBucket(string(b)) || !p.Key.Scope.HasAction(a) {
		return bucket.Info{}, errs.ErrAccessDenied
	}
	info, ok := f.infos[b]
	if !ok {
		return bucket.Info{}, errs.ErrNoSuchBucket
	}
	return info, nil
}

type fakeBodies struct {
	err error
}

func (f fakeBodies) OpenBody(r *http.Request, _ sigv4.Auth) (io.ReadCloser, error) {
	if f.err != nil {
		return nil, f.err
	}
	return r.Body, nil
}

type fixture struct {
	handlers  *Handlers
	evaluator *fakeEvaluator
}

type fixtureOptions struct {
	infos     map[bucket.BucketName]bucket.Info
	cors      router.CORSConfig
	region    string
	evalErr   error
	bodiesErr error
}

func newFixture(t *testing.T, opts fixtureOptions) *fixture {
	t.Helper()
	if opts.infos == nil {
		opts.infos = map[bucket.BucketName]bucket.Info{"photos": {Name: "photos", Policy: bucket.PolicyPrivate, DefaultEncryption: bucket.EncryptionNone}}
	}
	evaluator := &fakeEvaluator{infos: opts.infos, err: opts.evalErr}
	handlers, err := NewHandlers(Dependencies{
		Buckets:   fakeBuckets{infos: opts.infos},
		Evaluator: evaluator,
		Bodies:    fakeBodies{err: opts.bodiesErr},
		Config:    Config{Region: opts.region, OwnerID: testOwnerID, CORS: opts.cors},
	})
	if err != nil {
		t.Fatalf("NewHandlers: %v", err)
	}
	return &fixture{handlers: handlers, evaluator: evaluator}
}

func authFor(buckets []string, actions []string) sigv4.Auth {
	scope, err := access.NewScope(buckets, actions)
	if err != nil {
		panic(err)
	}
	return sigv4.Auth{Kind: sigv4.AuthHeader, Key: access.KeyInfo{ID: "BDBKEXAMPLE", Scope: scope}}
}

func fullAuth() sigv4.Auth {
	return authFor([]string{"*"}, []string{"read", "list", "write", "delete"})
}

func routeFor(op router.Operation, subresource string) router.Route {
	return router.Route{Target: router.Target{Bucket: "photos"}, Operation: op, Subresource: subresource}
}

func call(h router.HandlerFunc, method, target, body string, headers map[string]string, rt router.Route, a sigv4.Auth) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	for name, value := range headers {
		r.Header.Set(name, value)
	}
	rec := httptest.NewRecorder()
	h(rec, r, rt, a)
	return rec
}

func anonymousAuth() sigv4.Auth {
	return sigv4.Auth{Kind: sigv4.AuthAnonymous}
}
