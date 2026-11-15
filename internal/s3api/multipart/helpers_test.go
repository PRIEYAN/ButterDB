package multipart

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"io"
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
	"github.com/PRIEYAN/ButterDB/internal/ingest/multipartstage"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/readpath/objectreader"
	"github.com/PRIEYAN/ButterDB/internal/s3api/router"
)

const (
	testBucket    = "photos"
	testKey       = "video/clip.mp4"
	testUploadID  = "4c1f0a9e7d2b6e8a3f5c1d0b9a7e6f2c"
	testAccessKey = "BDBK7Q2M9X4T1W8Z3R6Y"
	testOwnerID   = "5d0a000000000000000000000000000000000000000000000000000000000064"
	testChannelID = 4242
)

var testNow = time.Date(2026, 11, 13, 10, 5, 40, 0, time.UTC)

type putCall struct {
	uploadID string
	number   int
	size     int64
	body     []byte
	err      error
}

type fakeStage struct {
	mu            sync.Mutex
	createRequest []multipartstage.CreateRequest
	createErr     error
	parts         map[int]multipartstage.Part
	putErr        error
	putCalls      []putCall
	completeFn    func(ctx context.Context, id string, parts []multipartstage.CompletedPart, cond multipartstage.Conditions) (object.ObjectMeta, error)
	completeCalls int
	completeParts []multipartstage.CompletedPart
	completeCond  multipartstage.Conditions
	abortErr      error
	abortCalls    int
	listPagesFn   func(marker, max int) (multipartstage.PartPage, error)
	listPartsArgs [][2]int
	uploadsPage   multipartstage.UploadPage
	uploadsQuery  multipartstage.UploadQuery
	uploadsBucket int64
}

func (f *fakeStage) Create(_ context.Context, req multipartstage.CreateRequest) (multipartstage.Upload, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createRequest = append(f.createRequest, req)
	if f.createErr != nil {
		return multipartstage.Upload{}, f.createErr
	}
	return multipartstage.Upload{ID: testUploadID, Bucket: req.Bucket, Key: req.Key, State: stateOpen}, nil
}

func (f *fakeStage) PutPart(_ context.Context, uploadID string, number int, size int64, body io.Reader) (multipartstage.Part, error) {
	data, readErr := io.ReadAll(body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.putCalls = append(f.putCalls, putCall{uploadID: uploadID, number: number, size: size, body: data, err: readErr})
	if readErr != nil {
		return multipartstage.Part{}, readErr
	}
	if f.putErr != nil {
		return multipartstage.Part{}, f.putErr
	}
	sum := md5.Sum(data)
	part := multipartstage.Part{Number: number, Size: int64(len(data)), ETag: hex.EncodeToString(sum[:]), StagedAt: testNow}
	if f.parts == nil {
		f.parts = make(map[int]multipartstage.Part)
	}
	f.parts[number] = part
	return part, nil
}

func (f *fakeStage) Complete(ctx context.Context, id string, parts []multipartstage.CompletedPart, cond multipartstage.Conditions) (object.ObjectMeta, error) {
	f.mu.Lock()
	f.completeCalls++
	f.completeParts = parts
	f.completeCond = cond
	fn := f.completeFn
	f.mu.Unlock()
	if fn == nil {
		return object.ObjectMeta{}, errors.New("complete is not scripted")
	}
	return fn(ctx, id, parts, cond)
}

func (f *fakeStage) Abort(_ context.Context, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.abortCalls++
	return f.abortErr
}

func (f *fakeStage) ListParts(_ context.Context, _ string, marker int, max int) (multipartstage.PartPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listPartsArgs = append(f.listPartsArgs, [2]int{marker, max})
	if f.listPagesFn != nil {
		return f.listPagesFn(marker, max)
	}
	page := multipartstage.PartPage{}
	for number := marker + 1; number <= 10000 && len(page.Parts) < max; number++ {
		if part, ok := f.parts[number]; ok {
			page.Parts = append(page.Parts, part)
		}
	}
	return page, nil
}

func (f *fakeStage) ListUploads(_ context.Context, channelID int64, q multipartstage.UploadQuery) (multipartstage.UploadPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uploadsBucket = channelID
	f.uploadsQuery = q
	return f.uploadsPage, nil
}

func (f *fakeStage) setParts(parts ...multipartstage.Part) {
	f.parts = make(map[int]multipartstage.Part, len(parts))
	for _, part := range parts {
		f.parts[part.Number] = part
	}
}

type fakeUploads struct {
	records map[string]UploadRecord
}

func (f fakeUploads) LookupUpload(_ context.Context, id string) (UploadRecord, error) {
	record, ok := f.records[id]
	if !ok {
		return UploadRecord{}, errs.ErrNoSuchUpload
	}
	return record, nil
}

type fakeAuthorizer struct {
	infos   map[bucket.BucketName]bucket.Info
	actions []access.Action
}

func (f *fakeAuthorizer) Authorize(_ context.Context, p policy.Principal, b bucket.BucketName, a access.Action) (bucket.Info, error) {
	f.actions = append(f.actions, a)
	if !p.Key.Scope.Allows(string(b), a) {
		return bucket.Info{}, errs.ErrAccessDenied
	}
	info, ok := f.infos[b]
	if !ok {
		return bucket.Info{}, errs.ErrNoSuchBucket
	}
	return info, nil
}

type fakeSources struct {
	objects map[string]fakeSourceObject
}

type fakeSourceObject struct {
	meta object.ObjectMeta
	data []byte
}

func (f fakeSources) Open(_ context.Context, bucketName, key string) (object.ObjectMeta, objectreader.Opened, error) {
	found, ok := f.objects[bucketName+"/"+key]
	if !ok {
		return object.ObjectMeta{}, nil, errs.ErrNoSuchKey
	}
	return found.meta, bytesOpened{data: found.data}, nil
}

type bytesOpened struct {
	data []byte
}

func (b bytesOpened) ReadAt(_ context.Context, dst []byte, off int64) (int, error) {
	if off >= int64(len(b.data)) {
		return 0, io.EOF
	}
	n := copy(dst, b.data[off:])
	if off+int64(n) >= int64(len(b.data)) {
		return n, io.EOF
	}
	return n, nil
}

func (b bytesOpened) Close() error { return nil }

type stubSecrets struct{}

func (stubSecrets) Secret(context.Context, access.AccessKeyID) ([]byte, access.KeyInfo, error) {
	return nil, access.KeyInfo{}, errs.ErrUnknownAccessKey
}

type fixture struct {
	handlers   *Handlers
	stage      *fakeStage
	authorizer *fakeAuthorizer
	uploads    fakeUploads
}

type fixtureOptions struct {
	config  Config
	records map[string]UploadRecord
	infos   map[bucket.BucketName]bucket.Info
	sources map[string]fakeSourceObject
}

func openRecord() UploadRecord {
	return UploadRecord{
		ID:                   testUploadID,
		BucketChannelID:      testChannelID,
		Bucket:               testBucket,
		Key:                  testKey,
		InitiatorAccessKeyID: testAccessKey,
		InitiatedAt:          testNow,
		State:                stateOpen,
	}
}

func newFixture(t *testing.T, opts fixtureOptions) *fixture {
	t.Helper()
	if opts.records == nil {
		opts.records = map[string]UploadRecord{testUploadID: openRecord()}
	}
	if opts.infos == nil {
		opts.infos = map[bucket.BucketName]bucket.Info{
			testBucket: {Name: testBucket, ChannelID: testChannelID, Policy: bucket.PolicyPrivate, DefaultEncryption: bucket.EncryptionNone},
			"archive":  {Name: "archive", ChannelID: 7, Policy: bucket.PolicyPrivate, DefaultEncryption: bucket.EncryptionNone},
		}
	}
	if opts.config.OwnerID == "" {
		opts.config.OwnerID = testOwnerID
	}
	stage := &fakeStage{}
	authorizer := &fakeAuthorizer{infos: opts.infos}
	uploads := fakeUploads{records: opts.records}
	verifier := sigv4.NewVerifier(sigv4.Config{Now: func() time.Time { return testNow }}, stubSecrets{})
	handlers, err := NewHandlers(Dependencies{
		Stage:      stage,
		Verifier:   verifier,
		Clock:      clock.NewFake(testNow),
		Config:     opts.config,
		Authorizer: authorizer,
		Uploads:    uploads,
		Sources:    fakeSources{objects: opts.sources},
	})
	if err != nil {
		t.Fatalf("NewHandlers: %v", err)
	}
	return &fixture{handlers: handlers, stage: stage, authorizer: authorizer, uploads: uploads}
}

func authFor(buckets []string, actions []string) sigv4.Auth {
	scope, err := access.NewScope(buckets, actions)
	if err != nil {
		panic(err)
	}
	return sigv4.Auth{
		Kind:          sigv4.AuthHeader,
		Mode:          sigv4.PayloadUnsigned,
		DecodedLength: -1,
		Key:           access.KeyInfo{ID: testAccessKey, Name: "uploader", Scope: scope},
	}
}

func writerAuth() sigv4.Auth {
	return authFor([]string{"*"}, []string{"read", "list", "write", "delete"})
}

func routeFor(op router.Operation) router.Route {
	return router.Route{
		Target:    router.Target{Bucket: testBucket, Key: testKey, HasKey: true},
		Operation: op,
	}
}

func bucketRoute(op router.Operation) router.Route {
	return router.Route{Target: router.Target{Bucket: testBucket}, Operation: op}
}

type requestSpec struct {
	method  string
	target  string
	body    string
	headers map[string]string
}

func (s requestSpec) build() *http.Request {
	r := httptest.NewRequest(s.method, s.target, strings.NewReader(s.body))
	for name, value := range s.headers {
		r.Header.Set(name, value)
	}
	return r
}

func serve(h router.HandlerFunc, spec requestSpec, rt router.Route, a sigv4.Auth) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	rec.Header().Set("X-Amz-Request-Id", "4F2A9C1D7E3B5A60")
	rec.Header().Set("X-Amz-Id-2", "9Kp3m1sQ0n2bW8vJcX4yZ6tR5uA7eD0fG1hI2jK3lM4=")
	h(rec, spec.build(), rt, a)
	return rec
}

type errorBody struct {
	XMLName xml.Name `xml:"Error"`
	Code    string   `xml:"Code"`
	Message string   `xml:"Message"`
	Extra   []struct {
		XMLName xml.Name
		Value   string `xml:",chardata"`
	} `xml:",any"`
}

func decodeError(t *testing.T, body []byte) errorBody {
	t.Helper()
	text := string(body)
	if index := strings.Index(text, "<Error>"); index >= 0 {
		text = text[index:]
	}
	var parsed errorBody
	if err := xml.Unmarshal([]byte(text), &parsed); err != nil {
		t.Fatalf("decode error body %q: %v", body, err)
	}
	return parsed
}

func (e errorBody) field(name string) string {
	for _, extra := range e.Extra {
		if extra.XMLName.Local == name {
			return extra.Value
		}
	}
	return ""
}

func assertErrorCode(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) errorBody {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d, body %s", rec.Code, status, rec.Body.String())
	}
	parsed := decodeError(t, rec.Body.Bytes())
	if parsed.Code != code {
		t.Fatalf("error code = %q, want %q, body %s", parsed.Code, code, rec.Body.String())
	}
	return parsed
}

func md5Hex(data []byte) string {
	sum := md5.Sum(data)
	return hex.EncodeToString(sum[:])
}

func repeatBytes(b byte, n int) []byte {
	return bytes.Repeat([]byte{b}, n)
}

type countingReader struct {
	src  io.Reader
	read int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.src.Read(p)
	c.read += n
	return n, err
}

func mustDecodeHex(text string) []byte {
	raw, err := hex.DecodeString(text)
	if err != nil {
		panic(err)
	}
	return raw
}

func partOfSize(number int, size int64) multipartstage.Part {
	return multipartstage.Part{Number: number, Size: size, ETag: strings.Repeat("0", 32), StagedAt: testNow}
}
