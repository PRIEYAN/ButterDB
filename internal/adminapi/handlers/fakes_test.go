package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/internal/adminapi/setup"
	"github.com/PRIEYAN/ButterDB/internal/domain/access"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/identity"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/storage/adminrepo"
)

var errNoKey = errs.ErrAccessKeyNotFound

type fakeBuckets struct {
	list    []bucket.Info
	info    bucket.Info
	err     error
	created []bucket.Spec
	patched []bucket.Patch
	deleted []bucket.BucketName
}

func (f *fakeBuckets) List(context.Context) ([]bucket.Info, error) { return f.list, f.err }

func (f *fakeBuckets) Create(_ context.Context, spec bucket.Spec) (bucket.Info, error) {
	f.created = append(f.created, spec)
	if f.err != nil {
		return bucket.Info{}, f.err
	}
	info := f.info
	info.Name = spec.Name
	info.Policy = spec.Policy
	info.DefaultEncryption = spec.DefaultEncryption
	info.CacheMaxAge = spec.CacheMaxAge
	return info, nil
}

func (f *fakeBuckets) Get(_ context.Context, name bucket.BucketName) (bucket.Info, error) {
	if f.err != nil {
		return bucket.Info{}, f.err
	}
	info := f.info
	info.Name = name
	return info, nil
}

func (f *fakeBuckets) Update(_ context.Context, name bucket.BucketName, patch bucket.Patch) (bucket.Info, error) {
	f.patched = append(f.patched, patch)
	if f.err != nil {
		return bucket.Info{}, f.err
	}
	info := f.info
	info.Name = name
	if patch.Policy != nil {
		info.Policy = *patch.Policy
	}
	return info, nil
}

func (f *fakeBuckets) Delete(_ context.Context, name bucket.BucketName) error {
	f.deleted = append(f.deleted, name)
	return f.err
}

type fakeKeys struct {
	keys    map[access.AccessKeyID]access.KeyInfo
	order   []access.AccessKeyID
	secret  string
	err     error
	include []bool
	updates int
	revoked []access.AccessKeyID
}

func (f *fakeKeys) List(_ context.Context, includeRevoked bool) ([]access.KeyInfo, error) {
	f.include = append(f.include, includeRevoked)
	if f.err != nil {
		return nil, f.err
	}
	var out []access.KeyInfo
	for _, id := range f.order {
		info := f.keys[id]
		if info.Revoked() && !includeRevoked {
			continue
		}
		out = append(out, info)
	}
	return out, nil
}

func (f *fakeKeys) Create(_ context.Context, name string, scope access.Scope) (access.KeyInfo, string, error) {
	if f.err != nil {
		return access.KeyInfo{}, "", f.err
	}
	info := access.KeyInfo{ID: "BDBK7Q2M9X4T1W8Z3R6Y", Name: name, Scope: scope, CreatedAt: 1700000000000}
	f.put(info)
	return info, f.secret, nil
}

func (f *fakeKeys) Get(_ context.Context, id access.AccessKeyID) (access.KeyInfo, error) {
	if f.err != nil {
		return access.KeyInfo{}, f.err
	}
	info, ok := f.keys[id]
	if !ok {
		return access.KeyInfo{}, errNoKey
	}
	return info, nil
}

func (f *fakeKeys) Update(_ context.Context, id access.AccessKeyID, name *string, scope *access.Scope) (access.KeyInfo, error) {
	f.updates++
	info, err := f.Get(context.Background(), id)
	if err != nil {
		return access.KeyInfo{}, err
	}
	if name != nil {
		info.Name = *name
	}
	if scope != nil {
		info.Scope = *scope
	}
	f.put(info)
	return info, nil
}

func (f *fakeKeys) Revoke(_ context.Context, id access.AccessKeyID) (access.KeyInfo, error) {
	info, err := f.Get(context.Background(), id)
	if err != nil {
		return access.KeyInfo{}, err
	}
	info.RevokedAt = 1700000100000
	f.put(info)
	f.revoked = append(f.revoked, id)
	return info, nil
}

func (f *fakeKeys) put(info access.KeyInfo) {
	if f.keys == nil {
		f.keys = map[access.AccessKeyID]access.KeyInfo{}
	}
	if _, ok := f.keys[info.ID]; !ok {
		f.order = append(f.order, info.ID)
	}
	f.keys[info.ID] = info
}

type fakeLister struct {
	page     object.ListPage
	meta     object.ObjectMeta
	err      error
	metaErr  error
	queries  []object.ListQuery
	metaKeys []object.Key
}

func (f *fakeLister) List(_ context.Context, _ bucket.BucketName, q object.ListQuery) (object.ListPage, error) {
	f.queries = append(f.queries, q)
	return f.page, f.err
}

func (f *fakeLister) Meta(_ context.Context, _ bucket.BucketName, k object.Key) (object.ObjectMeta, error) {
	f.metaKeys = append(f.metaKeys, k)
	if f.metaErr != nil {
		return object.ObjectMeta{}, f.metaErr
	}
	meta := f.meta
	meta.Key = string(k)
	return meta, nil
}

type fakeWriter struct {
	mu         sync.Mutex
	puts       []object.PutRequest
	bodies     [][]byte
	deletes    []object.Key
	meta       object.ObjectMeta
	putErr     error
	deleteErrs map[object.Key]error
	gate       chan struct{}
	committed  bool
	onPut      func(req object.PutRequest, body []byte) (object.ObjectMeta, error)
}

func (f *fakeWriter) Put(_ context.Context, req object.PutRequest, body io.Reader) (object.ObjectMeta, error) {
	data, readErr := io.ReadAll(body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts = append(f.puts, req)
	f.bodies = append(f.bodies, data)
	if readErr != nil {
		return object.ObjectMeta{}, readErr
	}
	if f.onPut != nil {
		return f.onPut(req, data)
	}
	if f.putErr != nil {
		return object.ObjectMeta{}, f.putErr
	}
	if f.gate != nil {
		<-f.gate
	}
	f.committed = true
	meta := f.meta
	meta.Key = string(req.Key)
	return meta, nil
}

func (f *fakeWriter) Delete(_ context.Context, _ bucket.BucketName, k object.Key) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes = append(f.deletes, k)
	return f.deleteErrs[k]
}

type fakeServer struct {
	err    error
	served []object.Key
	status int
	body   string
}

func (f *fakeServer) ServeObject(w http.ResponseWriter, r *http.Request, _ bucket.BucketName, k object.Key) error {
	f.served = append(f.served, k)
	if f.err != nil {
		return f.err
	}
	if f.status != 0 {
		w.WriteHeader(f.status)
	}
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, f.body)
	}
	return nil
}

type fakePresigner struct {
	url     string
	expires time.Time
	err     error
	calls   int
	ttl     time.Duration
}

func (f *fakePresigner) PresignGet(_ context.Context, _ access.AccessKeyID, _ bucket.BucketName, _ object.Key, ttl time.Duration) (string, time.Time, error) {
	f.calls++
	f.ttl = ttl
	return f.url, f.expires, f.err
}

type fakeStatus struct {
	status adminv1.Status
	usage  adminv1.Usage
	err    error
}

func (f *fakeStatus) Status(context.Context) (adminv1.Status, error) { return f.status, f.err }
func (f *fakeStatus) Usage(context.Context) (adminv1.Usage, error)   { return f.usage, f.err }

type fakeAudit struct {
	mu      sync.Mutex
	records []AuditRecord
	page    adminv1.AuditPage
	query   adminv1.AuditQuery
	err     error
}

func (f *fakeAudit) Record(_ context.Context, e AuditRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records = append(f.records, e)
	return f.err
}

func (f *fakeAudit) Page(_ context.Context, q adminv1.AuditQuery) (adminv1.AuditPage, error) {
	f.query = q
	return f.page, f.err
}

func (f *fakeAudit) actions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.records))
	for _, rec := range f.records {
		out = append(out, rec.Action)
	}
	return out
}

type fakeRelay struct {
	snapshot identity.Snapshot
	err      error
	calls    []string
	apiID    int
	apiHash  string
	phone    string
	loginID  string
	code     string
	password string
}

func (f *fakeRelay) Status(context.Context) (identity.Snapshot, error) {
	f.calls = append(f.calls, "status")
	return f.snapshot, f.err
}

func (f *fakeRelay) SaveCredentials(_ context.Context, apiID int, apiHash string) error {
	f.calls = append(f.calls, "save")
	f.apiID, f.apiHash = apiID, apiHash
	return f.err
}

func (f *fakeRelay) DeleteCredentials(context.Context) error {
	f.calls = append(f.calls, "delete")
	return f.err
}

func (f *fakeRelay) Start(_ context.Context, phone string) (identity.Snapshot, error) {
	f.calls = append(f.calls, "start")
	f.phone = phone
	return f.snapshot, f.err
}

func (f *fakeRelay) SubmitCode(_ context.Context, loginID, code string) (identity.Snapshot, error) {
	f.calls = append(f.calls, "code")
	f.loginID, f.code = loginID, code
	return f.snapshot, f.err
}

func (f *fakeRelay) SubmitPassword(_ context.Context, loginID, password string) (identity.Snapshot, error) {
	f.calls = append(f.calls, "password")
	f.loginID, f.password = loginID, password
	return f.snapshot, f.err
}

func (f *fakeRelay) Cancel(context.Context) (identity.Snapshot, error) {
	f.calls = append(f.calls, "cancel")
	return f.snapshot, f.err
}

func (f *fakeRelay) Unlink(context.Context) (identity.Snapshot, error) {
	f.calls = append(f.calls, "unlink")
	return f.snapshot, f.err
}

type fakeSetup struct {
	info   adminv1.ServerInfo
	result adminv1.ClaimResult
	err    error
	relay  *fakeRelay
	token  string
	device adminv1.DeviceInfo
}

func (f *fakeSetup) ServerInfo(context.Context) (adminv1.ServerInfo, error) { return f.info, f.err }

func (f *fakeSetup) Claim(_ context.Context, token string, device adminv1.DeviceInfo) (adminv1.ClaimResult, error) {
	f.token, f.device = token, device
	return f.result, f.err
}

func (f *fakeSetup) Relay() setup.LoginRelay { return f.relay }

type fakePairing struct {
	expires  time.Time
	result   adminv1.DeviceTokenResult
	err      error
	delivery string
	code     string
	device   adminv1.DeviceInfo
}

func (f *fakePairing) Request(_ context.Context, delivery string, device adminv1.DeviceInfo) (time.Time, error) {
	f.delivery, f.device = delivery, device
	return f.expires, f.err
}

func (f *fakePairing) Complete(_ context.Context, code string, device adminv1.DeviceInfo) (adminv1.DeviceTokenResult, error) {
	f.code, f.device = code, device
	return f.result, f.err
}

type fakeDevices struct {
	devices     []adminrepo.Device
	renamed     map[string]string
	revoked     []string
	confirmLast []bool
	rotated     []string
	err         error
}

func (f *fakeDevices) List(context.Context) ([]adminrepo.Device, error) { return f.devices, f.err }

func (f *fakeDevices) Rename(_ context.Context, id, name string) (adminrepo.Device, error) {
	if f.err != nil {
		return adminrepo.Device{}, f.err
	}
	if f.renamed == nil {
		f.renamed = map[string]string{}
	}
	f.renamed[id] = name
	return adminrepo.Device{DeviceID: id, Name: name, CreatedVia: adminrepo.CreatedViaSetup}, nil
}

func (f *fakeDevices) Revoke(_ context.Context, id string, confirmLast bool) error {
	f.revoked = append(f.revoked, id)
	f.confirmLast = append(f.confirmLast, confirmLast)
	return f.err
}

func (f *fakeDevices) Rotate(_ context.Context, id string) (adminrepo.Device, string, error) {
	f.rotated = append(f.rotated, id)
	if f.err != nil {
		return adminrepo.Device{}, "", f.err
	}
	return adminrepo.Device{DeviceID: id, Name: "laptop", CreatedVia: adminrepo.CreatedViaSetup}, "bdt1_rotated", nil
}

type countingReader struct{ next byte }

func (c *countingReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = c.next
		c.next++
	}
	return len(p), nil
}

type harness struct {
	t       *testing.T
	handler http.Handler
	clock   *clock.Fake
	buckets *fakeBuckets
	keys    *fakeKeys
	lister  *fakeLister
	writer  *fakeWriter
	server  *fakeServer
	presign *fakePresigner
	status  *fakeStatus
	audit   *fakeAudit
	setup   *fakeSetup
	pairing *fakePairing
	devices *fakeDevices
	device  adminrepo.Device
	config  Config
	reject  bool
}

type deviceContextKey struct{}

var testStart = time.Date(2026, 11, 12, 9, 41, 7, 0, time.UTC)

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		t:       t,
		clock:   clock.NewFake(testStart),
		buckets: &fakeBuckets{},
		keys:    &fakeKeys{secret: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"},
		lister:  &fakeLister{},
		writer:  &fakeWriter{},
		server:  &fakeServer{body: "bytes"},
		presign: &fakePresigner{url: "https://s3.example.com/photos/a?X-Amz-Signature=x", expires: testStart.Add(time.Hour)},
		status:  &fakeStatus{},
		audit:   &fakeAudit{},
		setup:   &fakeSetup{relay: &fakeRelay{}},
		pairing: &fakePairing{},
		devices: &fakeDevices{},
		device:  adminrepo.Device{DeviceID: "a1b2c3d4e5f6g7h2", Name: "Office laptop"},
		config:  Config{S3Endpoint: "https://s3.example.com", S3Region: "us-east-1"},
	}
	h.build()
	return h
}

func (h *harness) build() {
	h.t.Helper()
	handler, err := NewRouter(h.dependencies())
	if err != nil {
		h.t.Fatalf("NewRouter() error = %v", err)
	}
	h.handler = handler
}

func (h *harness) dependencies() Dependencies {
	return Dependencies{
		Buckets: h.buckets,
		Keys:    h.keys,
		Lister:  h.lister,
		Writer:  h.writer,
		Server:  h.server,
		Presign: h.presign,
		Status:  h.status,
		Audit:   h.audit,
		Setup:   h.setup,
		Pairing: h.pairing,
		Devices: h.devices,
		Device: func(ctx context.Context) (adminrepo.Device, bool) {
			dev, ok := ctx.Value(deviceContextKey{}).(adminrepo.Device)
			return dev, ok
		},
		Auth: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if h.reject {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				ctx := context.WithValue(r.Context(), deviceContextKey{}, h.device)
				next.ServeHTTP(w, r.WithContext(ctx))
			})
		},
		Clock:  h.clock,
		Random: &countingReader{},
		Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Config: h.config,
	}
}

type request struct {
	method  string
	path    string
	body    any
	raw     string
	headers map[string]string
	reader  io.Reader
	length  int64
}

func (h *harness) do(req request) *httptest.ResponseRecorder {
	h.t.Helper()
	var reader io.Reader
	switch {
	case req.reader != nil:
		reader = req.reader
	case req.raw != "":
		reader = strings.NewReader(req.raw)
	case req.body != nil:
		data, err := json.Marshal(req.body)
		if err != nil {
			h.t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(data)
	}
	r := httptest.NewRequest(req.method, req.path, reader)
	if req.body != nil || req.raw != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if req.reader != nil {
		r.ContentLength = req.length
	}
	for name, value := range req.headers {
		r.Header.Set(name, value)
	}
	w := httptest.NewRecorder()
	h.handler.ServeHTTP(w, r)
	return w
}

func decodeBody[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	return out
}

func decodeError(t *testing.T, w *httptest.ResponseRecorder) adminv1.ErrorBody {
	t.Helper()
	return decodeBody[adminv1.ErrorEnvelope](t, w).Error
}

func assertError(t *testing.T, w *httptest.ResponseRecorder, status int, code adminv1.Code) adminv1.ErrorBody {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status = %d, want %d; body %s", w.Code, status, w.Body.String())
	}
	body := decodeError(t, w)
	if body.Code != code {
		t.Fatalf("code = %q, want %q; body %s", body.Code, code, w.Body.String())
	}
	if w.Header().Get(adminv1.HeaderErrorCode) != string(code) {
		t.Fatalf("%s header = %q, want %q", adminv1.HeaderErrorCode, w.Header().Get(adminv1.HeaderErrorCode), code)
	}
	if body.RequestID == "" || body.RequestID != w.Header().Get(adminv1.HeaderRequestID) {
		t.Fatalf("request id body %q header %q", body.RequestID, w.Header().Get(adminv1.HeaderRequestID))
	}
	return body
}
