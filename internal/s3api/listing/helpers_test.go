package listing

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
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
	"github.com/PRIEYAN/ButterDB/internal/s3api/router"
)

const (
	testBucket    = "photos"
	testChannelID = 4242
	testOwnerID   = "5d0a000000000000000000000000000000000000000000000000000000000064"
)

var _ PolicyEvaluator = (*fakeEvaluator)(nil)
var _ ObjectLister = (*memoryLister)(nil)

type fakeEvaluator struct {
	infos map[bucket.BucketName]bucket.Info
	err   error
}

func (f *fakeEvaluator) Authorize(_ context.Context, p policy.Principal, b bucket.BucketName, a access.Action) (bucket.Info, error) {
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

func (f *fakeEvaluator) VisibleBuckets(_ context.Context, p policy.Principal) ([]bucket.Info, error) {
	if p.Anonymous {
		return nil, errs.ErrAccessDenied
	}
	var visible []bucket.Info
	for name, info := range f.infos {
		if p.Key.Scope.CoversBucket(string(name)) {
			visible = append(visible, info)
		}
	}
	return visible, nil
}

type memoryLister struct {
	mu    sync.Mutex
	metas []object.ObjectMeta
	calls []object.ListQuery
	err   error
}

func newMemoryLister(keys ...string) *memoryLister {
	lister := &memoryLister{}
	for i, key := range keys {
		lister.metas = append(lister.metas, object.ObjectMeta{
			Key:           key,
			PutMsgID:      int64(i + 1),
			PlaintextSize: int64(len(key)),
			ETag:          hex.EncodeToString([]byte("0123456789abcdef")),
			MsgDate:       time.Date(2026, 11, 12, 9, 41, 0, 0, time.UTC).Unix(),
		})
	}
	sort.Slice(lister.metas, func(i, j int) bool { return lister.metas[i].Key < lister.metas[j].Key })
	return lister
}

func (m *memoryLister) List(_ context.Context, channelID int64, query object.ListQuery) (object.ListPage, error) {
	m.mu.Lock()
	m.calls = append(m.calls, query)
	m.mu.Unlock()
	if m.err != nil {
		return object.ListPage{}, m.err
	}
	if channelID != testChannelID {
		return object.ListPage{}, errs.ErrNoSuchBucket
	}
	resumeValue, resumeIsPrefix, err := resumePoint(query)
	if err != nil {
		return object.ListPage{}, err
	}
	type entry struct {
		meta     *object.ObjectMeta
		prefix   string
		resumeAt string
		isPrefix bool
	}
	var entries []entry
	for i := range m.metas {
		key := m.metas[i].Key
		if !strings.HasPrefix(key, query.Prefix) || !afterResume(key, resumeValue, resumeIsPrefix) {
			continue
		}
		if common, rolled := rollUp(key, query); rolled {
			if len(entries) > 0 && entries[len(entries)-1].isPrefix && entries[len(entries)-1].prefix == common {
				continue
			}
			entries = append(entries, entry{prefix: common, resumeAt: common, isPrefix: true})
			continue
		}
		entries = append(entries, entry{meta: &m.metas[i], resumeAt: key})
	}
	var page object.ListPage
	if len(entries) > query.Limit {
		entries = entries[:query.Limit]
		last := entries[len(entries)-1]
		kind := byte(0x00)
		if last.isPrefix {
			kind = 0x01
		}
		page.NextToken = base64.RawURLEncoding.EncodeToString(append([]byte{0x01, kind}, last.resumeAt...))
	}
	for _, e := range entries {
		if e.isPrefix {
			page.CommonPrefixes = append(page.CommonPrefixes, e.prefix)
		} else {
			page.Objects = append(page.Objects, *e.meta)
		}
	}
	return page, nil
}

func resumePoint(query object.ListQuery) (string, bool, error) {
	if query.ContinuationToken != "" {
		raw, err := base64.RawURLEncoding.DecodeString(query.ContinuationToken)
		if err != nil || len(raw) <= 2 || raw[0] != 0x01 || raw[1] > 0x01 {
			return "", false, ErrInvalidContinuationToken
		}
		return string(raw[2:]), raw[1] == 0x01, nil
	}
	return query.StartAfter, false, nil
}

func afterResume(key, value string, isPrefix bool) bool {
	if value == "" {
		return true
	}
	if isPrefix {
		return key > value && !strings.HasPrefix(key, value)
	}
	return key > value
}

func rollUp(key string, query object.ListQuery) (string, bool) {
	if query.Delimiter == "" {
		return "", false
	}
	remainder := strings.TrimPrefix(key, query.Prefix)
	at := strings.Index(remainder, query.Delimiter)
	if at < 0 {
		return "", false
	}
	return query.Prefix + remainder[:at+len(query.Delimiter)], true
}

type fixture struct {
	handlers  *Handlers
	evaluator *fakeEvaluator
	lister    *memoryLister
}

func newFixture(t *testing.T, lister *memoryLister, infos ...bucket.Info) *fixture {
	t.Helper()
	if len(infos) == 0 {
		infos = []bucket.Info{{Name: testBucket, ChannelID: testChannelID, Policy: bucket.PolicyPrivate, DefaultEncryption: bucket.EncryptionNone, CreatedDate: time.Date(2026, 11, 12, 9, 41, 0, 0, time.UTC).Unix()}}
	}
	byName := map[bucket.BucketName]bucket.Info{}
	for _, info := range infos {
		byName[info.Name] = info
	}
	if lister == nil {
		lister = newMemoryLister()
	}
	evaluator := &fakeEvaluator{infos: byName}
	handlers, err := NewHandlers(Dependencies{
		Evaluator: evaluator,
		Lister:    lister,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Config:    Config{Region: "us-east-1", OwnerID: testOwnerID},
	})
	if err != nil {
		t.Fatalf("NewHandlers: %v", err)
	}
	return &fixture{handlers: handlers, evaluator: evaluator, lister: lister}
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

func (f *fixture) do(t *testing.T, handler router.HandlerFunc, op router.Operation, target string, auth sigv4.Auth) *httptest.ResponseRecorder {
	t.Helper()
	bucketName := ""
	if op != router.OpListBuckets {
		bucketName = testBucket
	}
	r := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	handler(rec, r, router.Route{Target: router.Target{Bucket: bucketName}, Operation: op}, auth)
	return rec
}

func (f *fixture) listV2(t *testing.T, query string) *httptest.ResponseRecorder {
	t.Helper()
	return f.do(t, f.handlers.ListObjectsV2, router.OpListObjectsV2, "/photos?list-type=2"+query, fullAuth())
}

func (f *fixture) listV1(t *testing.T, query string) *httptest.ResponseRecorder {
	t.Helper()
	return f.do(t, f.handlers.ListObjects, router.OpListObjects, "/photos?"+strings.TrimPrefix(query, "&"), fullAuth())
}

type listedObject struct {
	Key          string `xml:"Key"`
	LastModified string `xml:"LastModified"`
	ETag         string `xml:"ETag"`
	Size         int64  `xml:"Size"`
	StorageClass string `xml:"StorageClass"`
	Owner        *struct {
		ID          string `xml:"ID"`
		DisplayName string `xml:"DisplayName"`
	} `xml:"Owner"`
}

type listDocument struct {
	XMLName               xml.Name       `xml:"ListBucketResult"`
	Name                  string         `xml:"Name"`
	Prefix                string         `xml:"Prefix"`
	Delimiter             *string        `xml:"Delimiter"`
	Marker                *string        `xml:"Marker"`
	NextMarker            *string        `xml:"NextMarker"`
	MaxKeys               int            `xml:"MaxKeys"`
	KeyCount              *int           `xml:"KeyCount"`
	IsTruncated           bool           `xml:"IsTruncated"`
	ContinuationToken     *string        `xml:"ContinuationToken"`
	NextContinuationToken *string        `xml:"NextContinuationToken"`
	StartAfter            *string        `xml:"StartAfter"`
	EncodingType          *string        `xml:"EncodingType"`
	Contents              []listedObject `xml:"Contents"`
	CommonPrefixes        []struct {
		Prefix string `xml:"Prefix"`
	} `xml:"CommonPrefixes"`
	raw string
}

func decodeList(t *testing.T, rec *httptest.ResponseRecorder) listDocument {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/xml" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if !strings.HasPrefix(rec.Body.String(), `<?xml version="1.0" encoding="UTF-8"?>`+"\n") {
		t.Fatalf("body lacks the xml declaration: %q", rec.Body.String())
	}
	var doc listDocument
	if err := xml.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	doc.raw = rec.Body.String()
	return doc
}

func (d listDocument) keys() []string {
	keys := make([]string, 0, len(d.Contents))
	for _, entry := range d.Contents {
		keys = append(keys, entry.Key)
	}
	return keys
}

func (d listDocument) prefixes() []string {
	prefixes := make([]string, 0, len(d.CommonPrefixes))
	for _, entry := range d.CommonPrefixes {
		prefixes = append(prefixes, entry.Prefix)
	}
	return prefixes
}

func equalStrings(got, want []string) bool {
	return slices.Equal(got, want) || (len(got) == 0 && len(want) == 0)
}

func errorCodeOf(t *testing.T, rec *httptest.ResponseRecorder) (string, string) {
	t.Helper()
	var doc struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	if err := xml.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("body %q is not an error document: %v", rec.Body.String(), err)
	}
	return doc.Code, doc.Message
}

func anonymousAuth() sigv4.Auth {
	return sigv4.Auth{Kind: sigv4.AuthAnonymous}
}
