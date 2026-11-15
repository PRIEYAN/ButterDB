package listing

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/s3api/router"
)

func sampleLister() *memoryLister {
	return newMemoryLister("a.txt", "photos/2026/a.jpg", "photos/2026/b.jpg", "photos/cover.jpg", "z.txt")
}

func TestV2Delimiter(t *testing.T) {
	cases := []struct {
		name         string
		query        string
		wantKeys     []string
		wantPrefixes []string
	}{
		{"root level", "&delimiter=%2F", []string{"a.txt", "z.txt"}, []string{"photos/"}},
		{"under a prefix", "&prefix=photos%2F&delimiter=%2F", []string{"photos/cover.jpg"}, []string{"photos/2026/"}},
		{"no delimiter lists everything", "", []string{"a.txt", "photos/2026/a.jpg", "photos/2026/b.jpg", "photos/cover.jpg", "z.txt"}, nil},
		{"prefix without delimiter", "&prefix=photos%2F2026", []string{"photos/2026/a.jpg", "photos/2026/b.jpg"}, nil},
		{"empty delimiter is none", "&delimiter=", []string{"a.txt", "photos/2026/a.jpg", "photos/2026/b.jpg", "photos/cover.jpg", "z.txt"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, sampleLister())
			doc := decodeList(t, f.listV2(t, tc.query))
			if !equalStrings(doc.keys(), tc.wantKeys) || !equalStrings(doc.prefixes(), tc.wantPrefixes) {
				t.Fatalf("keys = %v prefixes = %v", doc.keys(), doc.prefixes())
			}
			if doc.KeyCount == nil || *doc.KeyCount != len(tc.wantKeys)+len(tc.wantPrefixes) || doc.IsTruncated {
				t.Fatalf("KeyCount = %v truncated = %v", doc.KeyCount, doc.IsTruncated)
			}
			if doc.Name != testBucket {
				t.Fatalf("Name = %q", doc.Name)
			}
		})
	}
}

func TestV2ResponseShape(t *testing.T) {
	f := newFixture(t, sampleLister())
	doc := decodeList(t, f.listV2(t, "&prefix=photos%2F&delimiter=%2F"))
	if len(doc.Contents) != 1 {
		t.Fatalf("contents = %+v", doc.Contents)
	}
	entry := doc.Contents[0]
	if entry.StorageClass != "STANDARD" || entry.LastModified != "2026-11-12T09:41:00.000Z" || entry.Size != int64(len("photos/cover.jpg")) {
		t.Fatalf("entry = %+v", entry)
	}
	if entry.ETag != `"30313233343536373839616263646566"` {
		t.Fatalf("ETag = %q", entry.ETag)
	}
	if entry.Owner != nil {
		t.Fatal("owner was included without fetch-owner")
	}
	if !strings.Contains(doc.raw, "<Prefix>photos/</Prefix>") || !strings.Contains(doc.raw, "<Delimiter>/</Delimiter>") || !strings.Contains(doc.raw, `xmlns="http://s3.amazonaws.com/doc/2006-03-01/"`) {
		t.Fatalf("raw = %q", doc.raw)
	}
	owner := decodeList(t, f.listV2(t, "&fetch-owner=true"))
	if owner.Contents[0].Owner == nil || owner.Contents[0].Owner.ID != testOwnerID || owner.Contents[0].Owner.DisplayName != "butterdb" {
		t.Fatalf("owner = %+v", owner.Contents[0].Owner)
	}
	empty := decodeList(t, f.listV2(t, "&prefix=nothing"))
	if !strings.Contains(empty.raw, "<Prefix>nothing</Prefix>") || empty.KeyCount == nil || *empty.KeyCount != 0 {
		t.Fatalf("empty = %q", empty.raw)
	}
	if empty.Delimiter != nil || empty.ContinuationToken != nil || empty.NextContinuationToken != nil || empty.StartAfter != nil || empty.EncodingType != nil {
		t.Fatalf("optional elements appeared: %q", empty.raw)
	}
	bare := decodeList(t, f.listV2(t, ""))
	if !strings.Contains(bare.raw, "<Prefix></Prefix>") {
		t.Fatalf("Prefix element must always be present: %q", bare.raw)
	}
}

func TestV2MultiCharDelimiter(t *testing.T) {
	f := newFixture(t, newMemoryLister("a--b--c", "a--d", "e", "ee--f"))
	doc := decodeList(t, f.listV2(t, "&delimiter=--"))
	if !equalStrings(doc.keys(), []string{"e"}) || !equalStrings(doc.prefixes(), []string{"a--", "ee--"}) {
		t.Fatalf("keys = %v prefixes = %v", doc.keys(), doc.prefixes())
	}
	if doc.Delimiter == nil || *doc.Delimiter != "--" {
		t.Fatalf("Delimiter = %v", doc.Delimiter)
	}
	tooLong := f.listV2(t, "&delimiter="+strings.Repeat("d", 1025))
	if code, _ := errorCodeOf(t, tooLong); code != "InvalidArgument" || tooLong.Code != 400 {
		t.Fatalf("status = %d code = %q", tooLong.Code, code)
	}
	if rec := f.listV2(t, "&delimiter="+strings.Repeat("d", 1024)); rec.Code != 200 {
		t.Fatalf("a 1024 byte delimiter was refused: %d", rec.Code)
	}
}

func TestV2MaxKeysCountsPrefixes(t *testing.T) {
	f := newFixture(t, sampleLister())
	doc := decodeList(t, f.listV2(t, "&delimiter=%2F&max-keys=2"))
	if !equalStrings(doc.keys(), []string{"a.txt"}) || !equalStrings(doc.prefixes(), []string{"photos/"}) {
		t.Fatalf("keys = %v prefixes = %v", doc.keys(), doc.prefixes())
	}
	if doc.KeyCount == nil || *doc.KeyCount != 2 || doc.MaxKeys != 2 || !doc.IsTruncated {
		t.Fatalf("KeyCount = %v MaxKeys = %d truncated = %v", doc.KeyCount, doc.MaxKeys, doc.IsTruncated)
	}
	if doc.NextContinuationToken == nil || *doc.NextContinuationToken == "" {
		t.Fatal("a truncated listing carries no NextContinuationToken")
	}
	if got := f.lister.calls[0].Limit; got != 2 {
		t.Fatalf("lister limit = %d", got)
	}
}

func TestV2ContinuationAfterCommonPrefix(t *testing.T) {
	f := newFixture(t, sampleLister())
	first := decodeList(t, f.listV2(t, "&delimiter=%2F&max-keys=2"))
	raw, err := base64.RawURLEncoding.DecodeString(*first.NextContinuationToken)
	if err != nil || len(raw) < 3 || raw[0] != 0x01 || raw[1] != 0x01 || string(raw[2:]) != "photos/" {
		t.Fatalf("token = %q (%v)", raw, err)
	}
	second := decodeList(t, f.listV2(t, "&delimiter=%2F&max-keys=2&continuation-token="+url.QueryEscape(*first.NextContinuationToken)))
	if !equalStrings(second.keys(), []string{"z.txt"}) || len(second.prefixes()) != 0 {
		t.Fatalf("second page keys = %v prefixes = %v", second.keys(), second.prefixes())
	}
	if second.IsTruncated || second.NextContinuationToken != nil {
		t.Fatal("the last page was marked truncated")
	}
	if second.ContinuationToken == nil || *second.ContinuationToken != *first.NextContinuationToken {
		t.Fatalf("ContinuationToken = %v", second.ContinuationToken)
	}
}

func TestV2StartAfterIgnoredWithToken(t *testing.T) {
	keys := []string{"k1", "k2", "k3", "k4"}
	f := newFixture(t, newMemoryLister(keys...))
	first := decodeList(t, f.listV2(t, "&max-keys=2"))
	token := url.QueryEscape(*first.NextContinuationToken)
	withToken := decodeList(t, f.listV2(t, "&max-keys=2&start-after=k3&continuation-token="+token))
	if !equalStrings(withToken.keys(), []string{"k3", "k4"}) {
		t.Fatalf("keys = %v, want the token to win over start-after", withToken.keys())
	}
	if call := f.lister.calls[len(f.lister.calls)-1]; call.StartAfter != "" || call.ContinuationToken == "" {
		t.Fatalf("lister query = %+v", call)
	}
	if withToken.StartAfter == nil || *withToken.StartAfter != "k3" {
		t.Fatalf("StartAfter echo = %v", withToken.StartAfter)
	}
	withoutToken := decodeList(t, f.listV2(t, "&start-after=k2"))
	if !equalStrings(withoutToken.keys(), []string{"k3", "k4"}) {
		t.Fatalf("keys = %v", withoutToken.keys())
	}
}

func TestV2EncodingTypeURL(t *testing.T) {
	f := newFixture(t, newMemoryLister("dir one/a+b.txt", "dir one/café.txt", "plain.txt"))
	encoded := decodeList(t, f.listV2(t, "&encoding-type=url&delimiter=%2F&start-after=a%2Bb"))
	if !equalStrings(encoded.keys(), []string{"plain.txt"}) || !equalStrings(encoded.prefixes(), []string{"dir+one%2F"}) {
		t.Fatalf("keys = %v prefixes = %v", encoded.keys(), encoded.prefixes())
	}
	if encoded.EncodingType == nil || *encoded.EncodingType != "url" || encoded.Delimiter == nil || *encoded.Delimiter != "%2F" {
		t.Fatalf("EncodingType = %v Delimiter = %v", encoded.EncodingType, encoded.Delimiter)
	}
	if encoded.StartAfter == nil || *encoded.StartAfter != "a%2Bb" {
		t.Fatalf("StartAfter = %v", encoded.StartAfter)
	}
	inner := decodeList(t, f.listV2(t, "&encoding-type=url&prefix=dir+one%2F"))
	if !equalStrings(inner.keys(), []string{"dir+one%2Fa%2Bb.txt", "dir+one%2Fcaf%C3%A9.txt"}) {
		t.Fatalf("keys = %v", inner.keys())
	}
	if !strings.Contains(inner.raw, "<Prefix>dir+one%2F</Prefix>") {
		t.Fatalf("raw = %q", inner.raw)
	}
	plain := decodeList(t, f.listV2(t, "&prefix=dir+one%2F"))
	if !equalStrings(plain.keys(), []string{"dir one/a+b.txt", "dir one/café.txt"}) || plain.EncodingType != nil {
		t.Fatalf("keys = %v encoding = %v", plain.keys(), plain.EncodingType)
	}
	bad := f.listV2(t, "&encoding-type=base64")
	if code, _ := errorCodeOf(t, bad); code != "InvalidArgument" || bad.Code != 400 {
		t.Fatalf("status = %d code = %q", bad.Code, code)
	}
}

func TestV2MaxKeysZero(t *testing.T) {
	f := newFixture(t, sampleLister())
	doc := decodeList(t, f.listV2(t, "&max-keys=0"))
	if doc.KeyCount == nil || *doc.KeyCount != 0 || doc.IsTruncated || len(doc.Contents) != 0 || doc.MaxKeys != 0 {
		t.Fatalf("doc = %+v", doc)
	}
	if len(f.lister.calls) != 0 {
		t.Fatal("max-keys=0 reached the lister")
	}
}

func TestV2MaxKeysClamped(t *testing.T) {
	cases := []struct {
		name      string
		query     string
		wantLimit int
		wantMax   int
		wantCode  int
	}{
		{"default", "", 1000, 1000, 200},
		{"above the cap is clamped", "&max-keys=5000", 1000, 1000, 200},
		{"exactly the cap", "&max-keys=1000", 1000, 1000, 200},
		{"overflowing integer is clamped", "&max-keys=99999999999999999999", 1000, 1000, 200},
		{"small value", "&max-keys=7", 7, 7, 200},
		{"negative", "&max-keys=-1", 0, 0, 400},
		{"not an integer", "&max-keys=ten", 0, 0, 400},
		{"empty", "&max-keys=", 0, 0, 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, sampleLister())
			rec := f.listV2(t, tc.query)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d; body=%q", rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.wantCode == 400 {
				if code, _ := errorCodeOf(t, rec); code != "InvalidArgument" {
					t.Fatalf("code = %q", code)
				}
				return
			}
			doc := decodeList(t, rec)
			if doc.MaxKeys != tc.wantMax || f.lister.calls[0].Limit != tc.wantLimit {
				t.Fatalf("MaxKeys = %d limit = %d", doc.MaxKeys, f.lister.calls[0].Limit)
			}
		})
	}
}

func TestV2PagingWalksEveryKey(t *testing.T) {
	var keys []string
	for i := 0; i < 25; i++ {
		keys = append(keys, fmt.Sprintf("dir%02d/file", i%5), fmt.Sprintf("top%02d", i))
	}
	f := newFixture(t, newMemoryLister(keys...))
	var seen []string
	query := "&delimiter=%2F&max-keys=4"
	for pages := 0; pages < 20; pages++ {
		doc := decodeList(t, f.listV2(t, query))
		seen = append(seen, doc.prefixes()...)
		seen = append(seen, doc.keys()...)
		if !doc.IsTruncated {
			break
		}
		query = "&delimiter=%2F&max-keys=4&continuation-token=" + url.QueryEscape(*doc.NextContinuationToken)
	}
	if len(seen) != 5+25 {
		t.Fatalf("saw %d entries: %v", len(seen), seen)
	}
}

func TestBadContinuationToken(t *testing.T) {
	cases := []struct {
		name   string
		token  string
		lister error
	}{
		{"not base64", "!!!", nil},
		{"wrong version", base64.RawURLEncoding.EncodeToString([]byte{0x09, 0x00, 'k'}), nil},
		{"too short", base64.RawURLEncoding.EncodeToString([]byte{0x01}), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, sampleLister())
			rec := f.listV2(t, "&continuation-token="+url.QueryEscape(tc.token))
			code, message := errorCodeOf(t, rec)
			if rec.Code != 400 || code != "InvalidArgument" || message != "The continuation token provided is incorrect" {
				t.Fatalf("status = %d code = %q message = %q", rec.Code, code, message)
			}
		})
	}
	t.Run("only the sentinel is recognised", func(t *testing.T) {
		f := newFixture(t, sampleLister())
		f.lister.err = errs.ErrSlowDown
		rec := f.listV2(t, "")
		if code, _ := errorCodeOf(t, rec); rec.Code != 503 || code != "SlowDown" {
			t.Fatalf("status = %d code = %q", rec.Code, code)
		}
	})
}

func TestV1Basics(t *testing.T) {
	f := newFixture(t, sampleLister())
	doc := decodeList(t, f.listV1(t, "delimiter=%2F"))
	if !equalStrings(doc.keys(), []string{"a.txt", "z.txt"}) || !equalStrings(doc.prefixes(), []string{"photos/"}) {
		t.Fatalf("keys = %v prefixes = %v", doc.keys(), doc.prefixes())
	}
	if doc.KeyCount != nil || doc.ContinuationToken != nil || doc.NextContinuationToken != nil {
		t.Fatalf("v2 elements in a v1 listing: %q", doc.raw)
	}
	if doc.Marker == nil || *doc.Marker != "" || !strings.Contains(doc.raw, "<Marker></Marker>") {
		t.Fatalf("Marker must always be present: %q", doc.raw)
	}
}

func TestV1OwnerAlwaysPresent(t *testing.T) {
	f := newFixture(t, sampleLister())
	doc := decodeList(t, f.listV1(t, ""))
	if len(doc.Contents) != 5 {
		t.Fatalf("contents = %d", len(doc.Contents))
	}
	for _, entry := range doc.Contents {
		if entry.Owner == nil || entry.Owner.ID != testOwnerID || entry.Owner.DisplayName != "butterdb" {
			t.Fatalf("owner missing on %q: %+v", entry.Key, entry.Owner)
		}
	}
}

func TestV1NextMarkerOnlyWithDelimiter(t *testing.T) {
	f := newFixture(t, sampleLister())
	cases := []struct {
		name       string
		query      string
		wantMarker string
		wantSet    bool
	}{
		{"truncated with a delimiter ending in a common prefix", "delimiter=%2F&max-keys=2", "photos/", true},
		{"truncated with a delimiter ending in a key", "delimiter=%2F&max-keys=1", "a.txt", true},
		{"truncated without a delimiter", "max-keys=2", "", false},
		{"not truncated with a delimiter", "delimiter=%2F", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := decodeList(t, f.listV1(t, tc.query))
			if (doc.NextMarker != nil) != tc.wantSet || (tc.wantSet && *doc.NextMarker != tc.wantMarker) {
				t.Fatalf("NextMarker = %v, want set=%v %q; raw=%q", doc.NextMarker, tc.wantSet, tc.wantMarker, doc.raw)
			}
		})
	}
}

func TestV1MarkerSemantics(t *testing.T) {
	f := newFixture(t, sampleLister())
	t.Run("plain marker lists keys after it", func(t *testing.T) {
		doc := decodeList(t, f.listV1(t, "marker=photos%2F2026%2Fa.jpg"))
		if !equalStrings(doc.keys(), []string{"photos/2026/b.jpg", "photos/cover.jpg", "z.txt"}) {
			t.Fatalf("keys = %v", doc.keys())
		}
		if doc.Marker == nil || *doc.Marker != "photos/2026/a.jpg" {
			t.Fatalf("Marker = %v", doc.Marker)
		}
	})
	t.Run("a common prefix marker skips every key beginning with it", func(t *testing.T) {
		doc := decodeList(t, f.listV1(t, "delimiter=%2F&marker=photos%2F"))
		if !equalStrings(doc.keys(), []string{"z.txt"}) || len(doc.prefixes()) != 0 {
			t.Fatalf("keys = %v prefixes = %v", doc.keys(), doc.prefixes())
		}
	})
	t.Run("a marker that is not a common prefix is a plain key", func(t *testing.T) {
		doc := decodeList(t, f.listV1(t, "marker=photos%2F"))
		if !equalStrings(doc.keys(), []string{"photos/2026/a.jpg", "photos/2026/b.jpg", "photos/cover.jpg", "z.txt"}) {
			t.Fatalf("keys = %v", doc.keys())
		}
	})
	t.Run("paging with next marker walks to the end", func(t *testing.T) {
		marker := ""
		var seen []string
		for pages := 0; pages < 10; pages++ {
			doc := decodeList(t, f.listV1(t, "delimiter=%2F&max-keys=1&marker="+url.QueryEscape(marker)))
			seen = append(seen, doc.prefixes()...)
			seen = append(seen, doc.keys()...)
			if !doc.IsTruncated {
				break
			}
			marker = *doc.NextMarker
		}
		if !equalStrings(seen, []string{"a.txt", "photos/", "z.txt"}) {
			t.Fatalf("seen = %v", seen)
		}
	})
}

func TestV1EncodingTypeURL(t *testing.T) {
	f := newFixture(t, newMemoryLister("a b/1", "a b/2", "c d"))
	doc := decodeList(t, f.listV1(t, "encoding-type=url&delimiter=%2F&max-keys=1&marker=a+b%2F"))
	if doc.Marker == nil || *doc.Marker != "a+b%2F" {
		t.Fatalf("Marker = %v", doc.Marker)
	}
	if !equalStrings(doc.keys(), []string{"c+d"}) {
		t.Fatalf("keys = %v", doc.keys())
	}
	truncated := decodeList(t, f.listV1(t, "encoding-type=url&delimiter=%2F&max-keys=1"))
	if truncated.NextMarker == nil || *truncated.NextMarker != "a+b%2F" {
		t.Fatalf("NextMarker = %v", truncated.NextMarker)
	}
}

func TestV1MaxKeysZero(t *testing.T) {
	f := newFixture(t, sampleLister())
	doc := decodeList(t, f.listV1(t, "max-keys=0&delimiter=%2F"))
	if doc.IsTruncated || len(doc.Contents) != 0 || doc.NextMarker != nil {
		t.Fatalf("doc = %q", doc.raw)
	}
	if len(f.lister.calls) != 0 {
		t.Fatal("max-keys=0 reached the lister")
	}
}

func TestListObjectsAccess(t *testing.T) {
	f := newFixture(t, sampleLister())
	cases := []struct {
		name     string
		auth     func() (bool, []string, []string)
		status   int
		wantCode string
	}{
		{"key without list", func() (bool, []string, []string) { return false, []string{"*"}, []string{"read", "write"} }, 403, "AccessDenied"},
		{"bucket outside scope", func() (bool, []string, []string) { return false, []string{"other"}, []string{"list"} }, 403, "AccessDenied"},
		{"anonymous", func() (bool, []string, []string) { return true, nil, nil }, 403, "AccessDenied"},
	}
	for _, tc := range cases {
		for _, version := range []string{"v1", "v2"} {
			t.Run(tc.name+" "+version, func(t *testing.T) {
				anonymous, buckets, actions := tc.auth()
				auth := anonymousAuth()
				if !anonymous {
					auth = authFor(buckets, actions)
				}
				rec := f.do(t, f.handlers.ListObjectsV2, router.OpListObjectsV2, "/photos?list-type=2", auth)
				if version == "v1" {
					rec = f.do(t, f.handlers.ListObjects, router.OpListObjects, "/photos", auth)
				}
				if code, _ := errorCodeOf(t, rec); rec.Code != tc.status || code != tc.wantCode {
					t.Fatalf("status = %d code = %q", rec.Code, code)
				}
			})
		}
	}
	if len(f.lister.calls) != 0 {
		t.Fatal("denied requests reached the lister")
	}
	missing := newFixture(t, sampleLister())
	missing.evaluator.infos = nil
	rec := missing.listV2(t, "")
	if code, _ := errorCodeOf(t, rec); rec.Code != 404 || code != "NoSuchBucket" {
		t.Fatalf("status = %d code = %q", rec.Code, code)
	}
}
