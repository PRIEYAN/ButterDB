package listing

import (
	"encoding/base64"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/s3api/router"
)

type bucketsDocument struct {
	XMLName xml.Name `xml:"ListAllMyBucketsResult"`
	Owner   struct {
		ID          string `xml:"ID"`
		DisplayName string `xml:"DisplayName"`
	} `xml:"Owner"`
	Buckets []struct {
		Name         string `xml:"Name"`
		CreationDate string `xml:"CreationDate"`
		BucketRegion string `xml:"BucketRegion"`
	} `xml:"Buckets>Bucket"`
	ContinuationToken *string `xml:"ContinuationToken"`
	Prefix            *string `xml:"Prefix"`
}

func bucketInfos(names ...string) []bucket.Info {
	infos := make([]bucket.Info, 0, len(names))
	for i, name := range names {
		infos = append(infos, bucket.Info{
			Name:        bucket.BucketName(name),
			ChannelID:   int64(100 + i),
			Policy:      bucket.PolicyPrivate,
			CreatedDate: time.Date(2026, 11, 12, 9, 41, int(i), 0, time.UTC).Unix(),
		})
	}
	return infos
}

func listBuckets(t *testing.T, f *fixture, query string, auth func() bool, scope ...string) *httptest.ResponseRecorder {
	t.Helper()
	a := fullAuth()
	if len(scope) > 0 {
		a = authFor(scope, []string{"read"})
	}
	if auth != nil && auth() {
		a = anonymousAuth()
	}
	r := httptest.NewRequest(http.MethodGet, "/"+query, nil)
	rec := httptest.NewRecorder()
	f.handlers.ListBuckets(rec, r, router.Route{Operation: router.OpListBuckets}, a)
	return rec
}

func decodeBuckets(t *testing.T, rec *httptest.ResponseRecorder) bucketsDocument {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%q", rec.Code, rec.Body.String())
	}
	if !strings.HasPrefix(rec.Body.String(), `<?xml version="1.0" encoding="UTF-8"?>`+"\n") {
		t.Fatalf("body = %q", rec.Body.String())
	}
	var doc bucketsDocument
	if err := xml.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	return doc
}

func names(doc bucketsDocument) []string {
	out := make([]string, 0, len(doc.Buckets))
	for _, entry := range doc.Buckets {
		out = append(out, entry.Name)
	}
	return out
}

func TestListBucketsSortedWithOwner(t *testing.T) {
	f := newFixture(t, nil, bucketInfos("zeta", "alpha", "mid")...)
	doc := decodeBuckets(t, listBuckets(t, f, "", nil))
	if !equalStrings(names(doc), []string{"alpha", "mid", "zeta"}) {
		t.Fatalf("names = %v", names(doc))
	}
	if doc.Owner.ID != testOwnerID || doc.Owner.DisplayName != "butterdb" {
		t.Fatalf("owner = %+v", doc.Owner)
	}
	for _, entry := range doc.Buckets {
		if entry.BucketRegion != "us-east-1" || !strings.HasPrefix(entry.CreationDate, "2026-11-12T09:41:") || !strings.HasSuffix(entry.CreationDate, ".000Z") {
			t.Fatalf("entry = %+v", entry)
		}
	}
	if doc.ContinuationToken != nil || doc.Prefix != nil {
		t.Fatal("optional elements appeared")
	}
}

func TestListBucketsScopeAndAnonymous(t *testing.T) {
	f := newFixture(t, nil, bucketInfos("alpha", "beta", "gamma")...)
	doc := decodeBuckets(t, listBuckets(t, f, "", nil, "beta", "gamma"))
	if !equalStrings(names(doc), []string{"beta", "gamma"}) {
		t.Fatalf("names = %v", names(doc))
	}
	rec := listBuckets(t, f, "", func() bool { return true })
	if code, _ := errorCodeOf(t, rec); rec.Code != 403 || code != "AccessDenied" {
		t.Fatalf("status = %d code = %q", rec.Code, code)
	}
}

func TestListBucketsPaging(t *testing.T) {
	f := newFixture(t, nil, bucketInfos("a1", "b2", "c3", "d4", "e5")...)
	first := decodeBuckets(t, listBuckets(t, f, "?max-buckets=2", nil))
	if !equalStrings(names(first), []string{"a1", "b2"}) || first.ContinuationToken == nil {
		t.Fatalf("first = %v token = %v", names(first), first.ContinuationToken)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(*first.ContinuationToken)
	if err != nil || string(decoded) != "b2" {
		t.Fatalf("token decodes to %q (%v)", decoded, err)
	}
	second := decodeBuckets(t, listBuckets(t, f, "?max-buckets=2&continuation-token="+url.QueryEscape(*first.ContinuationToken), nil))
	if !equalStrings(names(second), []string{"c3", "d4"}) || second.ContinuationToken == nil {
		t.Fatalf("second = %v", names(second))
	}
	third := decodeBuckets(t, listBuckets(t, f, "?max-buckets=2&continuation-token="+url.QueryEscape(*second.ContinuationToken), nil))
	if !equalStrings(names(third), []string{"e5"}) || third.ContinuationToken != nil {
		t.Fatalf("third = %v token = %v", names(third), third.ContinuationToken)
	}
}

func TestListBucketsQueryParameters(t *testing.T) {
	f := newFixture(t, nil, bucketInfos("logs-a", "logs-b", "media")...)
	cases := []struct {
		name       string
		query      string
		wantNames  []string
		wantPrefix string
		wantStatus int
	}{
		{"prefix filters and is echoed", "?prefix=logs", []string{"logs-a", "logs-b"}, "logs", 200},
		{"matching bucket region", "?bucket-region=us-east-1", []string{"logs-a", "logs-b", "media"}, "", 200},
		{"other bucket region is empty", "?bucket-region=eu-west-1", nil, "", 200},
		{"max-buckets zero", "?max-buckets=0", nil, "", 400},
		{"max-buckets above the cap", "?max-buckets=10001", nil, "", 400},
		{"max-buckets not an integer", "?max-buckets=many", nil, "", 400},
		{"max-buckets at the cap", "?max-buckets=10000", []string{"logs-a", "logs-b", "media"}, "", 200},
		{"malformed continuation token", "?continuation-token=***", nil, "", 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := listBuckets(t, f, tc.query, nil)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%q", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantStatus == 400 {
				if code, _ := errorCodeOf(t, rec); code != "InvalidArgument" {
					t.Fatalf("code = %q", code)
				}
				return
			}
			doc := decodeBuckets(t, rec)
			if !equalStrings(names(doc), tc.wantNames) {
				t.Fatalf("names = %v", names(doc))
			}
			if tc.wantPrefix != "" && (doc.Prefix == nil || *doc.Prefix != tc.wantPrefix) {
				t.Fatalf("Prefix = %v", doc.Prefix)
			}
		})
	}
}

func TestNewHandlersRejectsMissingDependencies(t *testing.T) {
	good := Dependencies{Evaluator: &fakeEvaluator{}, Lister: newMemoryLister(), Logger: newFixture(t, nil).handlers.deps.Logger, Config: Config{OwnerID: testOwnerID}}
	cases := []struct {
		name   string
		mutate func(*Dependencies)
	}{
		{"evaluator", func(d *Dependencies) { d.Evaluator = nil }},
		{"lister", func(d *Dependencies) { d.Lister = nil }},
		{"logger", func(d *Dependencies) { d.Logger = nil }},
		{"owner id", func(d *Dependencies) { d.Config.OwnerID = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := good
			tc.mutate(&deps)
			if _, err := NewHandlers(deps); err == nil {
				t.Fatal("NewHandlers accepted a missing dependency")
			}
		})
	}
	if _, err := NewHandlers(good); err != nil {
		t.Fatalf("NewHandlers: %v", err)
	}
	routes := newFixture(t, nil).handlers.Routes()
	for _, op := range []router.Operation{router.OpListBuckets, router.OpListObjectsV2, router.OpListObjects} {
		if routes[op] == nil {
			t.Fatalf("route for %s missing", op)
		}
	}
}
