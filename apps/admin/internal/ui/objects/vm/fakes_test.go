package vm

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
)

type putCall struct {
	Bucket string
	Key    string
	Size   int64
	Opts   adminv1.PutObjectOptions
	Body   []byte
}

type fakeClient struct {
	mu sync.Mutex

	buckets    []adminv1.Bucket
	pages      map[string]adminv1.ObjectList
	listCalls  []adminv1.ListObjectsOptions
	listErr    error
	existing   map[string]adminv1.ObjectMeta
	headErr    error
	headCalls  int
	status     adminv1.Status
	statusErr  error
	metas      map[string]adminv1.ObjectMeta
	streams    map[string]func() adminv1.ObjectStream
	getCalls   []string
	putHook    func(call int, ctx context.Context, body io.Reader, size int64, opts adminv1.PutObjectOptions) (adminv1.ObjectMeta, error)
	puts       []putCall
	deleted    [][]string
	deleteSolo []string
	deleteFn   func(batch []string) (adminv1.DeleteObjectsResult, error)
	keys       adminv1.AccessKeyList
	presignReq []adminv1.PresignRequest
}

func newFakeClient() *fakeClient {
	return &fakeClient{
		pages:    map[string]adminv1.ObjectList{},
		existing: map[string]adminv1.ObjectMeta{},
		metas:    map[string]adminv1.ObjectMeta{},
		streams:  map[string]func() adminv1.ObjectStream{},
		status:   adminv1.Status{Limits: adminv1.Limits{MaxObjectBytes: 1 << 20, MaxKeyBytes: 1024, MaxUserMetadataBytes: 2048, MaxDeleteBatch: 1000}},
	}
}

func (f *fakeClient) ListBuckets(context.Context) (adminv1.BucketList, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return adminv1.BucketList{Buckets: f.buckets}, nil
}

func (f *fakeClient) ListObjects(_ context.Context, _ string, opts adminv1.ListObjectsOptions) (adminv1.ObjectList, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls = append(f.listCalls, opts)
	if f.listErr != nil {
		return adminv1.ObjectList{}, f.listErr
	}
	return f.pages[opts.Cursor], nil
}

func (f *fakeClient) HeadObject(_ context.Context, _, key string) (adminv1.ObjectMeta, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.headCalls++
	if f.headErr != nil {
		return adminv1.ObjectMeta{}, f.headErr
	}
	if meta, ok := f.existing[key]; ok {
		return meta, nil
	}
	return adminv1.ObjectMeta{}, &adminv1.APIError{Status: http.StatusNotFound, Body: adminv1.ErrorBody{Code: adminv1.CodeObjectNotFound}}
}

func (f *fakeClient) ObjectMeta(_ context.Context, _, key string) (adminv1.ObjectMeta, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	meta, ok := f.metas[key]
	if !ok {
		return adminv1.ObjectMeta{}, &adminv1.APIError{Status: http.StatusNotFound, Body: adminv1.ErrorBody{Code: adminv1.CodeObjectNotFound}}
	}
	return meta, nil
}

func (f *fakeClient) GetObject(_ context.Context, _, key string, _ adminv1.GetObjectOptions) (adminv1.ObjectStream, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getCalls = append(f.getCalls, key)
	build, ok := f.streams[key]
	if !ok {
		return adminv1.ObjectStream{}, &adminv1.APIError{Status: http.StatusNotFound, Body: adminv1.ErrorBody{Code: adminv1.CodeObjectNotFound}}
	}
	return build(), nil
}

func (f *fakeClient) PutObject(ctx context.Context, bucket, key string, body io.Reader, size int64, opts adminv1.PutObjectOptions) (adminv1.ObjectMeta, error) {
	f.mu.Lock()
	call := len(f.puts)
	f.puts = append(f.puts, putCall{Bucket: bucket, Key: key, Size: size, Opts: opts})
	hook := f.putHook
	f.mu.Unlock()
	if hook != nil {
		return hook(call, ctx, body, size, opts)
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return adminv1.ObjectMeta{}, err
	}
	f.mu.Lock()
	f.puts[call].Body = data
	f.mu.Unlock()
	return adminv1.ObjectMeta{Key: key, ETag: `"stored"`}, nil
}

func (f *fakeClient) DeleteObject(_ context.Context, _, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteSolo = append(f.deleteSolo, key)
	return nil
}

func (f *fakeClient) DeleteObjects(_ context.Context, _ string, req adminv1.DeleteObjectsRequest) (adminv1.DeleteObjectsResult, error) {
	f.mu.Lock()
	f.deleted = append(f.deleted, req.Keys)
	fn := f.deleteFn
	f.mu.Unlock()
	if fn != nil {
		return fn(req.Keys)
	}
	return adminv1.DeleteObjectsResult{Deleted: req.Keys}, nil
}

func (f *fakeClient) Presign(_ context.Context, _ string, req adminv1.PresignRequest) (adminv1.PresignedURL, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.presignReq = append(f.presignReq, req)
	return adminv1.PresignedURL{URL: "https://s3.example/x", Method: "GET"}, nil
}

func (f *fakeClient) ListAccessKeys(context.Context, bool) (adminv1.AccessKeyList, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.keys, nil
}

func (f *fakeClient) Status(context.Context) (adminv1.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status, f.statusErr
}

func (f *fakeClient) putCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.puts)
}

func (f *fakeClient) putAt(i int) putCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.puts[i]
}

type countingRandom struct {
	mu sync.Mutex
	n  byte
}

func (r *countingRandom) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range p {
		r.n++
		p[i] = r.n
	}
	return len(p), nil
}

type sleepLog struct {
	mu    sync.Mutex
	waits []time.Duration
}

func (s *sleepLog) sleep(ctx context.Context, d time.Duration) error {
	s.mu.Lock()
	s.waits = append(s.waits, d)
	s.mu.Unlock()
	return ctx.Err()
}

func (s *sleepLog) all() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.waits...)
}

var fixedNow = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

func newTestVM(t *testing.T, c *fakeClient, opts ...Option) (*ViewModel, *sleepLog) {
	t.Helper()
	log := &sleepLog{}
	all := append([]Option{WithRandom(&countingRandom{}), WithSleep(log.sleep)}, opts...)
	return New(c, func() time.Time { return fixedNow }, all...), log
}

func writeTempFile(t *testing.T, name string, size int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	data := make([]byte, size)
	for i := range data {
		data[i] = byte('a' + i%26)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return path
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func apiErr(status int, code adminv1.Code) *adminv1.APIError {
	return &adminv1.APIError{Status: status, Body: adminv1.ErrorBody{Code: code}}
}

func transferByKey(list TransferList, key string) (Transfer, bool) {
	for _, item := range list.Items {
		if item.Key == key {
			return item, true
		}
	}
	return Transfer{}, false
}
