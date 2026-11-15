package objectops

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/access/policy"
	"github.com/PRIEYAN/ButterDB/internal/access/sigv4"
	"github.com/PRIEYAN/ButterDB/internal/domain/access"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/readpath/httprange"
	"github.com/PRIEYAN/ButterDB/internal/readpath/objectreader"
	"github.com/PRIEYAN/ButterDB/internal/s3api/router"
)

var (
	_ PayloadSource     = (*sigv4.Verifier)(nil)
	_ ObjectWriter      = (*fakeWriter)(nil)
	_ ObjectRemover     = (*fakeRemover)(nil)
	_ ObjectOpener      = (*fakeStore)(nil)
	_ ObjectHeads       = (*fakeStore)(nil)
	_ PrincipalAttacher = (*fakePrincipals)(nil)
	_ PolicyEvaluator   = (*fakeEvaluator)(nil)
)

const (
	testBucket    = "photos"
	testChannelID = 4242
	testRequestID = "4F2A9C1D7E3B5A60"
)

var testNow = time.Unix(1_800_000_000, 0).UTC()

type deadlineRecorder struct {
	*httptest.ResponseRecorder
	mu        sync.Mutex
	deadlines []time.Time
}

func newDeadlineRecorder() *deadlineRecorder {
	return &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func (r *deadlineRecorder) SetReadDeadline(deadline time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deadlines = append(r.deadlines, deadline)
	return nil
}

type fakeEvaluator struct {
	infos   map[bucket.BucketName]bucket.Info
	calls   []access.Action
	denyNth int
	mu      sync.Mutex
}

func (f *fakeEvaluator) Authorize(_ context.Context, p policy.Principal, b bucket.BucketName, a access.Action) (bucket.Info, error) {
	f.mu.Lock()
	f.calls = append(f.calls, a)
	denied := f.denyNth != 0 && len(f.calls) == f.denyNth
	f.mu.Unlock()
	if denied {
		return bucket.Info{}, errs.ErrAccessDenied
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

func (f *fakeEvaluator) AuthorizeAnonymousRead(_ context.Context, b bucket.BucketName) (bucket.Info, error) {
	info, ok := f.infos[b]
	if !ok || info.Policy != bucket.PolicyPublicRead {
		return bucket.Info{}, errs.ErrAccessDenied
	}
	return info, nil
}

type memoryObject struct {
	meta object.ObjectMeta
	data []byte
}

type fakeStore struct {
	mu      sync.Mutex
	objects map[string]memoryObject
	opens   []string
	heads   []string
	opened  []*memoryOpened
	failure error
	cut     int
}

func objectID(bucketName, key string) string {
	return bucketName + "/" + key
}

func (s *fakeStore) add(bucketName, key string, data []byte, mutate func(*object.ObjectMeta)) {
	sum := md5.Sum(data)
	meta := object.ObjectMeta{
		ChannelID:     testChannelID,
		Key:           key,
		PutMsgID:      77,
		StoredSize:    int64(len(data)),
		PlaintextSize: int64(len(data)),
		ETag:          hex.EncodeToString(sum[:]),
		ContentType:   "text/plain",
		MsgDate:       testNow.Add(-time.Hour).Unix(),
	}
	if mutate != nil {
		mutate(&meta)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.objects == nil {
		s.objects = map[string]memoryObject{}
	}
	s.objects[objectID(bucketName, key)] = memoryObject{meta: meta, data: data}
}

func (s *fakeStore) Open(_ context.Context, bucketName, key string) (object.ObjectMeta, objectreader.Opened, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opens = append(s.opens, objectID(bucketName, key))
	stored, ok := s.objects[objectID(bucketName, key)]
	if !ok {
		return object.ObjectMeta{}, nil, errs.ErrNoSuchKey
	}
	opened := &memoryOpened{data: stored.data, failure: s.failure, cut: s.cut}
	s.opened = append(s.opened, opened)
	return stored.meta, opened, nil
}

func (s *fakeStore) Get(_ context.Context, _ int64, key string) (object.ObjectMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.heads = append(s.heads, key)
	stored, ok := s.objects[objectID(testBucket, key)]
	if !ok {
		return object.ObjectMeta{}, errs.ErrNoSuchKey
	}
	return stored.meta, nil
}

type memoryOpened struct {
	data    []byte
	failure error
	cut     int
	closed  bool
}

func (o *memoryOpened) ReadAt(_ context.Context, dst []byte, off int64) (int, error) {
	if o.failure != nil && int(off) >= o.cut {
		return 0, o.failure
	}
	if off >= int64(len(o.data)) {
		return 0, io.EOF
	}
	end := int64(len(o.data))
	if o.failure != nil && int64(o.cut) < end {
		end = int64(o.cut)
	}
	n := copy(dst, o.data[off:end])
	return n, nil
}

func (o *memoryOpened) Close() error {
	o.closed = true
	return nil
}

type putCall struct {
	req  object.PutRequest
	body []byte
	ctx  context.Context
}

type fakeWriter struct {
	mu    sync.Mutex
	calls []putCall
	err   error
	store *fakeStore
	meta  func(object.PutRequest, []byte) object.ObjectMeta
}

func (f *fakeWriter) Put(ctx context.Context, req object.PutRequest, body io.Reader) (object.ObjectMeta, error) {
	data, err := io.ReadAll(body)
	f.mu.Lock()
	f.calls = append(f.calls, putCall{req: req, body: data, ctx: ctx})
	f.mu.Unlock()
	if err != nil {
		return object.ObjectMeta{}, err
	}
	if f.err != nil {
		return object.ObjectMeta{}, f.err
	}
	sum := md5.Sum(data)
	meta := object.ObjectMeta{
		Key:           string(req.Key),
		ETag:          hex.EncodeToString(sum[:]),
		PlaintextSize: int64(len(data)),
		MsgDate:       testNow.Unix(),
		Encrypted:     req.Encryption == bucket.EncryptionTDE1,
		ContentType:   req.ContentType,
		UserMeta:      req.UserMeta,
		SystemMeta:    req.SystemMeta,
	}
	if f.meta != nil {
		meta = f.meta(req, data)
	}
	if f.store != nil {
		f.store.add(req.Bucket, string(req.Key), data, func(m *object.ObjectMeta) { *m = meta })
	}
	return meta, nil
}

func (f *fakeWriter) last(t *testing.T) putCall {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		t.Fatal("writer was not called")
	}
	return f.calls[len(f.calls)-1]
}

type removeCall struct {
	bucket string
	key    string
	ctx    context.Context
}

type fakeRemover struct {
	mu     sync.Mutex
	calls  []removeCall
	errFor map[string]error
}

func (f *fakeRemover) Delete(ctx context.Context, name bucket.BucketName, key object.Key) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, removeCall{bucket: string(name), key: string(key), ctx: ctx})
	return f.errFor[string(key)]
}

type principalCall struct {
	accessKeyID string
	requestID   string
}

type fakePrincipals struct {
	mu    sync.Mutex
	calls []principalCall
	err   error
}

type principalContextKey struct{}

func (f *fakePrincipals) WithPrincipal(ctx context.Context, accessKeyID, requestID string) (context.Context, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, principalCall{accessKeyID: accessKeyID, requestID: requestID})
	if f.err != nil {
		return nil, f.err
	}
	return context.WithValue(ctx, principalContextKey{}, accessKeyID), nil
}

type stubSecrets struct{}

func (stubSecrets) Secret(context.Context, access.AccessKeyID) ([]byte, access.KeyInfo, error) {
	return nil, access.KeyInfo{}, errors.New("secrets are not used by body verification")
}

type fixture struct {
	handlers   *Handlers
	evaluator  *fakeEvaluator
	writer     *fakeWriter
	remover    *fakeRemover
	store      *fakeStore
	principals *fakePrincipals
	clock      *clock.Fake
}

type fixtureOptions struct {
	infos map[bucket.BucketName]bucket.Info
	idle  time.Duration
}

func privateBucket() bucket.Info {
	return bucket.Info{Name: testBucket, ChannelID: testChannelID, Policy: bucket.PolicyPrivate, DefaultEncryption: bucket.EncryptionNone}
}

func newFixture(t *testing.T, opts fixtureOptions) *fixture {
	t.Helper()
	if opts.infos == nil {
		opts.infos = map[bucket.BucketName]bucket.Info{testBucket: privateBucket()}
	}
	store := &fakeStore{}
	f := &fixture{
		evaluator:  &fakeEvaluator{infos: opts.infos},
		writer:     &fakeWriter{store: store},
		remover:    &fakeRemover{},
		store:      store,
		principals: &fakePrincipals{},
		clock:      clock.NewFake(testNow),
	}
	handlers, err := NewHandlers(Dependencies{
		Evaluator:  f.evaluator,
		Payloads:   sigv4.NewVerifier(sigv4.Config{Now: f.clock.Now}, stubSecrets{}),
		Writer:     f.writer,
		Remover:    f.remover,
		Opener:     store,
		Heads:      store,
		Principals: f.principals,
		Streamer:   httprange.NewStreamer(),
		Clock:      f.clock,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Config:     Config{BodyIdleTimeout: opts.idle},
	})
	if err != nil {
		t.Fatalf("NewHandlers: %v", err)
	}
	f.handlers = handlers
	return f
}

func authFor(buckets []string, actions []string, decoded int64) sigv4.Auth {
	scope, err := access.NewScope(buckets, actions)
	if err != nil {
		panic(err)
	}
	return sigv4.Auth{
		Kind:          sigv4.AuthHeader,
		Mode:          sigv4.PayloadUnsigned,
		DecodedLength: decoded,
		Key:           access.KeyInfo{ID: "BDBKEXAMPLE", Scope: scope},
	}
}

func fullAuth(decoded int64) sigv4.Auth {
	return authFor([]string{"*"}, []string{"read", "list", "write", "delete"}, decoded)
}

func anonymousAuth() sigv4.Auth {
	return sigv4.Auth{Kind: sigv4.AuthAnonymous, Mode: sigv4.PayloadNone}
}

func routeFor(op router.Operation, key string) router.Route {
	return router.Route{Target: router.Target{Bucket: testBucket, Key: key, HasKey: key != ""}, Operation: op}
}

type request struct {
	method  string
	target  string
	body    string
	headers map[string]string
	auth    sigv4.Auth
	ctx     context.Context
}

func (f *fixture) do(t *testing.T, handler router.HandlerFunc, op router.Operation, key string, req request) *deadlineRecorder {
	t.Helper()
	r := httptest.NewRequest(req.method, req.target, strings.NewReader(req.body))
	for name, value := range req.headers {
		r.Header.Set(name, value)
	}
	if req.ctx != nil {
		r = r.WithContext(req.ctx)
	}
	rec := newDeadlineRecorder()
	rec.Header().Set("X-Amz-Request-Id", testRequestID)
	handler(rec, r, routeFor(op, key), req.auth)
	return rec
}

func (f *fixture) put(t *testing.T, key, body string, headers map[string]string) *deadlineRecorder {
	t.Helper()
	return f.do(t, f.handlers.PutObject, router.OpPutObject, key, request{
		method:  http.MethodPut,
		target:  "/" + testBucket + "/" + key,
		body:    body,
		headers: headers,
		auth:    fullAuth(int64(len(body))),
	})
}

func (f *fixture) get(t *testing.T, key string, headers map[string]string, auth sigv4.Auth, query string) *deadlineRecorder {
	t.Helper()
	return f.do(t, f.handlers.GetObject, router.OpGetObject, key, request{
		method:  http.MethodGet,
		target:  "/" + testBucket + "/" + key + query,
		headers: headers,
		auth:    auth,
	})
}

func (f *fixture) head(t *testing.T, key string, headers map[string]string, query string) *deadlineRecorder {
	t.Helper()
	return f.do(t, f.handlers.HeadObject, router.OpHeadObject, key, request{
		method:  http.MethodHead,
		target:  "/" + testBucket + "/" + key + query,
		headers: headers,
		auth:    fullAuth(0),
	})
}

type errorDocument struct {
	Code    string
	Message string
	Fields  map[string]string
}

func parseError(t *testing.T, body string) errorDocument {
	t.Helper()
	decoder := xml.NewDecoder(strings.NewReader(body))
	doc := errorDocument{Fields: map[string]string{}}
	var current string
	depth := 0
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("body %q is not an error document: %v", body, err)
		}
		switch element := token.(type) {
		case xml.StartElement:
			depth++
			if depth == 2 {
				current = element.Name.Local
			}
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 2 {
				doc.Fields[current] += string(element)
			}
		}
	}
	doc.Code = doc.Fields["Code"]
	doc.Message = doc.Fields["Message"]
	return doc
}

func expectError(t *testing.T, rec *deadlineRecorder, status int, code string) errorDocument {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d; body=%q", rec.Code, status, rec.Body.String())
	}
	doc := parseError(t, rec.Body.String())
	if doc.Code != code {
		t.Fatalf("code = %q, want %q; body=%q", doc.Code, code, rec.Body.String())
	}
	return doc
}

type countingReader struct {
	reader io.Reader
	reads  int
}

func (c *countingReader) Read(p []byte) (int, error) {
	c.reads++
	return c.reader.Read(p)
}

func bodyBytes(rec *deadlineRecorder) []byte {
	return bytes.Clone(rec.Body.Bytes())
}
