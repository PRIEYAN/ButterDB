package multipart

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/access/sigv4"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/limits"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/ingest/multipartstage"
	"github.com/PRIEYAN/ButterDB/internal/ingest/put"
	"github.com/PRIEYAN/ButterDB/internal/s3api/router"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

const (
	minPart  = limits.MinMultipartPartBytes
	xmlDecl  = `<?xml version="1.0" encoding="UTF-8"?>` + "\n"
	resultNS = `xmlns="http://s3.amazonaws.com/doc/2006-03-01/"`
)

type listedSpec struct {
	number   int
	etag     string
	checksum string
}

func completeXML(parts ...listedSpec) string {
	var b strings.Builder
	b.WriteString(`<CompleteMultipartUpload ` + resultNS + `>`)
	for _, part := range parts {
		fmt.Fprintf(&b, `<Part><PartNumber>%d</PartNumber><ETag>%s</ETag>`, part.number, part.etag)
		if part.checksum != "" {
			fmt.Fprintf(&b, `<ChecksumCRC32>%s</ChecksumCRC32>`, part.checksum)
		}
		b.WriteString(`</Part>`)
	}
	b.WriteString(`</CompleteMultipartUpload>`)
	return b.String()
}

func completeRequestSpec(body string, headers map[string]string) requestSpec {
	return requestSpec{method: http.MethodPost, target: "/photos/video/clip.mp4?uploadId=" + testUploadID, body: body, headers: headers}
}

func etagOf(seed string) string { return md5Hex([]byte(seed)) }

func stagedPartWith(number int, size int64, seed string) multipartstage.Part {
	return multipartstage.Part{Number: number, Size: size, ETag: etagOf(seed), StagedAt: testNow}
}

func multipartMeta(parts ...multipartstage.Part) object.ObjectMeta {
	sums := make([][16]byte, 0, len(parts))
	for _, part := range parts {
		sums = append(sums, [16]byte(mustDecodeHex(part.ETag)))
	}
	etag, err := object.MultipartETag(sums)
	if err != nil {
		panic(err)
	}
	return object.ObjectMeta{ChannelID: testChannelID, Key: testKey, ETag: etag, JobID: "job"}
}

func succeedWith(meta object.ObjectMeta) func(context.Context, string, []multipartstage.CompletedPart, multipartstage.Conditions) (object.ObjectMeta, error) {
	return func(context.Context, string, []multipartstage.CompletedPart, multipartstage.Conditions) (object.ObjectMeta, error) {
		return meta, nil
	}
}

func twoParts() (multipartstage.Part, multipartstage.Part) {
	return stagedPartWith(1, minPart, "one"), stagedPartWith(2, 10, "two")
}

func serveComplete(fx *fixture, spec requestSpec, a sigv4.Auth) *httptest.ResponseRecorder {
	return serve(fx.handlers.CompleteMultipartUpload, spec, routeFor(router.OpCompleteMultipartUpload), a)
}

func TestEntityTooSmall(t *testing.T) {
	tests := []struct {
		name     string
		sizes    []int64
		wantCode string
		wantPart string
	}{
		{name: "first of two parts is one byte short", sizes: []int64{minPart - 1, 10}, wantCode: "EntityTooSmall", wantPart: "1"},
		{name: "first of two parts is exactly the minimum", sizes: []int64{minPart, 1}},
		{name: "only the last part may be small", sizes: []int64{minPart, 1, 1}, wantCode: "EntityTooSmall", wantPart: "2"},
		{name: "a single small part is the last part", sizes: []int64{1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fx := newFixture(t, fixtureOptions{})
			var staged []multipartstage.Part
			var listed []listedSpec
			for i, size := range test.sizes {
				part := stagedPartWith(i+1, size, fmt.Sprintf("p%d", i))
				staged = append(staged, part)
				listed = append(listed, listedSpec{number: part.Number, etag: `"` + part.ETag + `"`})
			}
			fx.stage.setParts(staged...)
			fx.stage.completeFn = succeedWith(multipartMeta(staged...))
			rec := serveComplete(fx, completeRequestSpec(completeXML(listed...), nil), writerAuth())
			if test.wantCode == "" {
				if rec.Code != http.StatusOK || fx.stage.completeCalls != 1 {
					t.Fatalf("status = %d, complete calls = %d, body %s", rec.Code, fx.stage.completeCalls, rec.Body.String())
				}
				return
			}
			parsed := assertErrorCode(t, rec, http.StatusBadRequest, test.wantCode)
			if parsed.field("MinSizeAllowed") != "5242880" || parsed.field("PartNumber") != test.wantPart || parsed.field("ProposedSize") != fmt.Sprint(test.sizes[int(test.wantPart[0]-'1')]) || parsed.field("ETag") == "" {
				t.Fatalf("extra fields = %+v", parsed)
			}
			if fx.stage.completeCalls != 0 {
				t.Fatalf("job started for a request that fails validation")
			}
			if strings.HasPrefix(rec.Body.String(), xmlDecl+" ") {
				t.Fatalf("validation failure used the long response")
			}
		})
	}
}

func TestInvalidPartOrder(t *testing.T) {
	one, two := twoParts()
	tests := []struct {
		name     string
		listed   []listedSpec
		wantCode string
	}{
		{name: "ascending", listed: []listedSpec{{number: 1, etag: one.ETag}, {number: 2, etag: two.ETag}}},
		{name: "descending", listed: []listedSpec{{number: 2, etag: two.ETag}, {number: 1, etag: one.ETag}}, wantCode: "InvalidPartOrder"},
		{name: "duplicate", listed: []listedSpec{{number: 1, etag: one.ETag}, {number: 1, etag: one.ETag}}, wantCode: "InvalidPartOrder"},
		{name: "ascending then drop", listed: []listedSpec{{number: 1, etag: one.ETag}, {number: 2, etag: two.ETag}, {number: 1, etag: one.ETag}}, wantCode: "InvalidPartOrder"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fx := newFixture(t, fixtureOptions{})
			fx.stage.setParts(one, two)
			fx.stage.completeFn = succeedWith(multipartMeta(one, two))
			rec := serveComplete(fx, completeRequestSpec(completeXML(test.listed...), nil), writerAuth())
			if test.wantCode == "" {
				if rec.Code != http.StatusOK || fx.stage.completeCalls != 1 {
					t.Fatalf("status = %d calls = %d", rec.Code, fx.stage.completeCalls)
				}
				return
			}
			assertErrorCode(t, rec, http.StatusBadRequest, test.wantCode)
			if fx.stage.completeCalls != 0 {
				t.Fatalf("job started for a mis-ordered list")
			}
		})
	}
}

func TestInvalidPartETag(t *testing.T) {
	one, two := twoParts()
	tests := []struct {
		name       string
		listed     []listedSpec
		wantNumber string
		wantOK     bool
	}{
		{name: "quoted lower case", listed: []listedSpec{{1, `"` + one.ETag + `"`, ""}, {2, `"` + two.ETag + `"`, ""}}, wantOK: true},
		{name: "unquoted", listed: []listedSpec{{1, one.ETag, ""}, {2, two.ETag, ""}}, wantOK: true},
		{name: "upper case hex", listed: []listedSpec{{1, strings.ToUpper(one.ETag), ""}, {2, two.ETag, ""}}, wantOK: true},
		{name: "wrong etag on the first part", listed: []listedSpec{{1, etagOf("other"), ""}, {2, two.ETag, ""}}, wantNumber: "1"},
		{name: "wrong etag on the last part", listed: []listedSpec{{1, one.ETag, ""}, {2, etagOf("other"), ""}}, wantNumber: "2"},
		{name: "part that was never staged", listed: []listedSpec{{1, one.ETag, ""}, {3, two.ETag, ""}}, wantNumber: "3"},
		{name: "empty etag", listed: []listedSpec{{1, "", ""}, {2, two.ETag, ""}}, wantNumber: "1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fx := newFixture(t, fixtureOptions{})
			fx.stage.setParts(one, two)
			fx.stage.completeFn = succeedWith(multipartMeta(one, two))
			rec := serveComplete(fx, completeRequestSpec(completeXML(test.listed...), nil), writerAuth())
			if test.wantOK {
				if rec.Code != http.StatusOK || fx.stage.completeCalls != 1 {
					t.Fatalf("status = %d calls = %d body %s", rec.Code, fx.stage.completeCalls, rec.Body.String())
				}
				return
			}
			parsed := assertErrorCode(t, rec, http.StatusBadRequest, "InvalidPart")
			if parsed.field("PartNumber") != test.wantNumber {
				t.Fatalf("PartNumber = %q, want %q", parsed.field("PartNumber"), test.wantNumber)
			}
			if fx.stage.completeCalls != 0 {
				t.Fatalf("job started for an invalid part")
			}
		})
	}
}

func TestCompleteListedChecksumVerified(t *testing.T) {
	one, two := twoParts()
	one.Checksum = "u1x2Pw=="
	tests := []struct {
		name       string
		recorded   string
		listed     string
		wantCode   string
		wantPassed string
	}{
		{name: "listed equals recorded", recorded: "u1x2Pw==", listed: "u1x2Pw==", wantPassed: "u1x2Pw=="},
		{name: "listed differs from recorded", recorded: "u1x2Pw==", listed: "AAAAAA==", wantCode: "InvalidPart"},
		{name: "nothing listed", recorded: "u1x2Pw==", listed: "", wantPassed: ""},
		{name: "nothing recorded drops the listed value", recorded: "", listed: "AAAAAA==", wantPassed: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fx := newFixture(t, fixtureOptions{})
			staged := one
			staged.Checksum = test.recorded
			fx.stage.setParts(staged, two)
			fx.stage.completeFn = succeedWith(multipartMeta(one, two))
			rec := serveComplete(fx, completeRequestSpec(completeXML(listedSpec{1, one.ETag, test.listed}, listedSpec{2, two.ETag, ""}), nil), writerAuth())
			if test.wantCode != "" {
				assertErrorCode(t, rec, http.StatusBadRequest, test.wantCode)
				if fx.stage.completeCalls != 0 {
					t.Fatalf("job started for a checksum mismatch")
				}
				return
			}
			if rec.Code != http.StatusOK || fx.stage.completeParts[0].Checksum != test.wantPassed {
				t.Fatalf("status = %d passed checksum = %q, want %q", rec.Code, fx.stage.completeParts[0].Checksum, test.wantPassed)
			}
		})
	}
}

func TestRunningTotalLimitAtComplete(t *testing.T) {
	fx := newFixture(t, fixtureOptions{})
	big := int64(limits.MaxStoredObjectBytes)/2 + 1
	a, b := stagedPartWith(1, big, "a"), stagedPartWith(2, big, "b")
	fx.stage.setParts(a, b)
	rec := serveComplete(fx, completeRequestSpec(completeXML(listedSpec{1, a.ETag, ""}, listedSpec{2, b.ETag, ""}), nil), writerAuth())
	assertErrorCode(t, rec, http.StatusBadRequest, "EntityTooLarge")
	if fx.stage.completeCalls != 0 {
		t.Fatalf("job started for an oversized object")
	}
}

func TestCompleteRequestValidation(t *testing.T) {
	one, two := twoParts()
	valid := completeXML(listedSpec{1, one.ETag, ""}, listedSpec{2, two.ETag, ""})
	tests := []struct {
		name       string
		body       string
		headers    map[string]string
		auth       func() sigv4.Auth
		wantStatus int
		wantCode   string
		wantCond   multipartstage.Conditions
	}{
		{name: "valid request", body: valid, wantStatus: 200},
		{name: "request without namespace", body: `<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><ETag>` + one.ETag + `</ETag></Part><Part><PartNumber>2</PartNumber><ETag>` + two.ETag + `</ETag></Part></CompleteMultipartUpload>`, wantStatus: 200},
		{name: "empty body", body: "", wantStatus: 400, wantCode: "MissingRequestBodyError"},
		{name: "malformed xml", body: "<CompleteMultipartUpload><Part>", wantStatus: 400, wantCode: "MalformedXML"},
		{name: "no parts", body: `<CompleteMultipartUpload></CompleteMultipartUpload>`, wantStatus: 400, wantCode: "MalformedXML"},
		{name: "doctype is refused", body: `<!DOCTYPE x [<!ENTITY a "b">]>` + valid, wantStatus: 400, wantCode: "MalformedXML"},
		{name: "body over four mebibytes", body: `<CompleteMultipartUpload>` + strings.Repeat(" ", 4<<20) + `</CompleteMultipartUpload>`, wantStatus: 400, wantCode: "MaxMessageLengthExceeded"},
		{name: "if-none-match star", body: valid, headers: map[string]string{"If-None-Match": "*"}, wantStatus: 200, wantCond: multipartstage.Conditions{IfNoneMatchAny: true}},
		{name: "if-none-match other value", body: valid, headers: map[string]string{"If-None-Match": `"abc"`}, wantStatus: 400, wantCode: "InvalidArgument"},
		{name: "if-match is passed on unquoted", body: valid, headers: map[string]string{"If-Match": `"abc"`}, wantStatus: 200, wantCond: multipartstage.Conditions{IfMatch: "abc"}},
		{name: "streaming payload is refused", body: valid, auth: func() sigv4.Auth {
			a := writerAuth()
			a.Mode = sigv4.PayloadStreamingUnsignedTrailer
			return a
		}, wantStatus: 400, wantCode: "InvalidRequest"},
		{name: "key without write scope", body: valid, auth: func() sigv4.Auth { return authFor([]string{"*"}, []string{"read"}) }, wantStatus: 403, wantCode: "AccessDenied"},
		{name: "anonymous", body: valid, auth: func() sigv4.Auth { return sigv4.Auth{} }, wantStatus: 403, wantCode: "AccessDenied"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fx := newFixture(t, fixtureOptions{})
			fx.stage.setParts(one, two)
			fx.stage.completeFn = succeedWith(multipartMeta(one, two))
			caller := writerAuth()
			if test.auth != nil {
				caller = test.auth()
			}
			rec := serveComplete(fx, completeRequestSpec(test.body, test.headers), caller)
			if test.wantCode != "" {
				assertErrorCode(t, rec, test.wantStatus, test.wantCode)
				if fx.stage.completeCalls != 0 {
					t.Fatalf("job started for a refused request")
				}
				return
			}
			if rec.Code != test.wantStatus || fx.stage.completeCalls != 1 {
				t.Fatalf("status = %d calls = %d body %s", rec.Code, fx.stage.completeCalls, rec.Body.String())
			}
			if fx.stage.completeCond != test.wantCond {
				t.Fatalf("conditions = %+v, want %+v", fx.stage.completeCond, test.wantCond)
			}
		})
	}
}

func TestCompleteUploadLookup(t *testing.T) {
	one, two := twoParts()
	valid := completeXML(listedSpec{1, one.ETag, ""}, listedSpec{2, two.ETag, ""})
	aborted := openRecord()
	aborted.State = stateAborted
	elsewhere := openRecord()
	elsewhere.Key = "other/key"
	tests := []struct {
		name    string
		records map[string]UploadRecord
		target  string
	}{
		{name: "aborted upload", records: map[string]UploadRecord{testUploadID: aborted}},
		{name: "upload of another key", records: map[string]UploadRecord{testUploadID: elsewhere}},
		{name: "unknown upload", records: map[string]UploadRecord{}},
		{name: "missing upload id", target: "/photos/video/clip.mp4"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fx := newFixture(t, fixtureOptions{records: test.records})
			fx.stage.setParts(one, two)
			spec := completeRequestSpec(valid, nil)
			if test.target != "" {
				spec.target = test.target
			}
			rec := serveComplete(fx, spec, writerAuth())
			assertErrorCode(t, rec, http.StatusNotFound, "NoSuchUpload")
			if fx.stage.completeCalls != 0 {
				t.Fatalf("job started for an unusable upload")
			}
		})
	}
}

func TestMultipartETag(t *testing.T) {
	one, two := twoParts()
	fx := newFixture(t, fixtureOptions{config: Config{PublicURL: "https://s3.example.com/"}})
	fx.stage.setParts(one, two)
	meta := multipartMeta(one, two)
	fx.stage.completeFn = succeedWith(meta)
	rec := serveComplete(fx, completeRequestSpec(completeXML(listedSpec{1, one.ETag, ""}, listedSpec{2, two.ETag, ""}), nil), writerAuth())
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Body.String(), xmlDecl) {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	var result completeResult
	document := rec.Body.String()
	if err := xml.Unmarshal([]byte(document[strings.Index(document, "<Complete"):]), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasSuffix(meta.ETag, "-2") || len(meta.ETag) != 32+len("-2") {
		t.Fatalf("composite etag = %q", meta.ETag)
	}
	if result.ETag != `"`+meta.ETag+`"` {
		t.Fatalf("ETag = %q, want %q", result.ETag, `"`+meta.ETag+`"`)
	}
	if result.Location != "https://s3.example.com/photos/video/clip.mp4" || result.Bucket != testBucket || result.Key != testKey {
		t.Fatalf("result = %+v", result)
	}
	if !strings.Contains(document, "<CompleteMultipartUploadResult "+resultNS+">") {
		t.Fatalf("namespace missing: %s", document)
	}
}

func TestCompleteLocation(t *testing.T) {
	one, two := twoParts()
	tests := []struct {
		name string
		tls  bool
		host string
		want string
	}{
		{name: "plain http request", host: "s3.local:9000", want: "http://s3.local:9000/photos/video/clip.mp4"},
		{name: "tls request", tls: true, host: "s3.local", want: "https://s3.local/photos/video/clip.mp4"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fx := newFixture(t, fixtureOptions{})
			fx.stage.setParts(one, two)
			fx.stage.completeFn = succeedWith(multipartMeta(one, two))
			r := completeRequestSpec(completeXML(listedSpec{1, one.ETag, ""}, listedSpec{2, two.ETag, ""}), nil).build()
			r.Host = test.host
			if test.tls {
				r.TLS = &tls.ConnectionState{}
			}
			rec := httptest.NewRecorder()
			fx.handlers.CompleteMultipartUpload(rec, r, routeFor(router.OpCompleteMultipartUpload), writerAuth())
			if !strings.Contains(rec.Body.String(), "<Location>"+test.want+"</Location>") {
				t.Fatalf("location missing %q in %s", test.want, rec.Body.String())
			}
		})
	}
}

func TestCompleteIdempotentRetry(t *testing.T) {
	one, two := twoParts()
	stored := multipartMeta(one, two)
	completed := openRecord()
	completed.State = stateCompleted
	completed.Encrypted = true
	sameList := completeXML(listedSpec{1, one.ETag, ""}, listedSpec{2, two.ETag, ""})
	otherList := completeXML(listedSpec{1, one.ETag, ""})
	tests := []struct {
		name     string
		body     string
		complete func(context.Context, string, []multipartstage.CompletedPart, multipartstage.Conditions) (object.ObjectMeta, error)
		wantCode string
	}{
		{name: "identical list returns the stored result", body: sameList, complete: succeedWith(stored)},
		{name: "different list is no such upload", body: otherList, complete: func(context.Context, string, []multipartstage.CompletedPart, multipartstage.Conditions) (object.ObjectMeta, error) {
			return object.ObjectMeta{}, errs.ErrNoSuchUpload
		}, wantCode: "NoSuchUpload"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fx := newFixture(t, fixtureOptions{records: map[string]UploadRecord{testUploadID: completed}})
			fx.stage.completeFn = test.complete
			rec := serveComplete(fx, completeRequestSpec(test.body, nil), writerAuth())
			if len(fx.stage.listPartsArgs) != 0 {
				t.Fatalf("staged parts consulted for a completed upload")
			}
			if fx.stage.completeCalls != 1 {
				t.Fatalf("complete calls = %d, want 1", fx.stage.completeCalls)
			}
			if test.wantCode != "" {
				assertErrorCode(t, rec, http.StatusNotFound, test.wantCode)
				return
			}
			if rec.Code != http.StatusOK || strings.HasPrefix(rec.Body.String(), xmlDecl+" ") {
				t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
			}
			if got := decodeResult[completeResult](t, rec.Body.Bytes(), "CompleteMultipartUploadResult").ETag; got != `"`+stored.ETag+`"` {
				t.Fatalf("stored result missing: %s", rec.Body.String())
			}
			if rec.Header().Get(serverSideEncryption) != "AES256" {
				t.Fatalf("encryption header missing on a replayed result")
			}
		})
	}
}

func TestCompleteAttachesToCommitting(t *testing.T) {
	one, two := twoParts()
	committing := openRecord()
	committing.State = stateCommitting
	fx := newFixture(t, fixtureOptions{records: map[string]UploadRecord{testUploadID: committing}})
	fx.stage.setParts(one, two)
	stored := multipartMeta(one, two)
	release := make(chan struct{})
	var mu sync.Mutex
	started := 0
	arrived := make(chan struct{}, 2)
	fx.stage.completeFn = func(ctx context.Context, _ string, _ []multipartstage.CompletedPart, _ multipartstage.Conditions) (object.ObjectMeta, error) {
		mu.Lock()
		started++
		mu.Unlock()
		arrived <- struct{}{}
		select {
		case <-release:
			return stored, nil
		case <-ctx.Done():
			return object.ObjectMeta{}, ctx.Err()
		}
	}
	body := completeXML(listedSpec{1, one.ETag, ""}, listedSpec{2, two.ETag, ""})
	results := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() { results <- serveComplete(fx, completeRequestSpec(body, nil), writerAuth()) }()
	}
	<-arrived
	<-arrived
	close(release)
	for range 2 {
		rec := <-results
		if rec.Code != http.StatusOK || decodeResult[completeResult](t, rec.Body.Bytes(), "CompleteMultipartUploadResult").ETag != `"`+stored.ETag+`"` {
			t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
		}
	}
	if started != 2 || fx.stage.completeCalls != 2 {
		t.Fatalf("handlers handed %d calls to the stage, want both requests attached", fx.stage.completeCalls)
	}
}

func TestCompleteSlowDownInBody(t *testing.T) {
	one, two := twoParts()
	tests := []struct {
		name     string
		err      error
		wantCode string
	}{
		{name: "flood wait past the budget", err: fmt.Errorf("multipartstage: complete: %w", errs.ErrSlowDown), wantCode: "SlowDown"},
		{name: "unknown send outcome", err: fmt.Errorf("multipartstage: complete: %w", &put.OutcomeUnknownError{JobID: "job", Err: tgclient.ErrSendOutcomeUnknown}), wantCode: "SlowDown"},
		{name: "write precondition failed", err: fmt.Errorf("multipartstage: complete: %w", errs.ErrPreconditionFailed), wantCode: "PreconditionFailed"},
		{name: "internal failure", err: io.ErrClosedPipe, wantCode: "InternalError"},
		{name: "upload became busy", err: errs.ErrUploadBusy, wantCode: "OperationAborted"},
		{name: "upload vanished", err: errs.ErrNoSuchUpload, wantCode: "NoSuchUpload"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fx := newFixture(t, fixtureOptions{})
			fx.stage.setParts(one, two)
			fx.stage.completeFn = func(context.Context, string, []multipartstage.CompletedPart, multipartstage.Conditions) (object.ObjectMeta, error) {
				return object.ObjectMeta{}, test.err
			}
			rec := serveComplete(fx, completeRequestSpec(completeXML(listedSpec{1, one.ETag, ""}, listedSpec{2, two.ETag, ""}), nil), writerAuth())
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want the 200 already sent", rec.Code)
			}
			body := rec.Body.String()
			if !strings.HasPrefix(body, xmlDecl) || strings.Count(body, "<?xml") != 1 {
				t.Fatalf("declaration must come once and first: %q", body)
			}
			if got := decodeError(t, rec.Body.Bytes()).Code; got != test.wantCode {
				t.Fatalf("error code = %q, want %q, body %s", got, test.wantCode, body)
			}
			if strings.Contains(body, "CompleteMultipartUploadResult") {
				t.Fatalf("failure carries a result element")
			}
		})
	}
}

func TestCompleteKeepaliveThenSuccess(t *testing.T) {
	one, two := twoParts()
	fx := newFixture(t, fixtureOptions{config: Config{KeepaliveInterval: 5 * time.Millisecond}})
	fx.stage.setParts(one, two)
	stored := multipartMeta(one, two)
	release := make(chan struct{})
	fx.stage.completeFn = func(ctx context.Context, _ string, _ []multipartstage.CompletedPart, _ multipartstage.Conditions) (object.ObjectMeta, error) {
		select {
		case <-release:
			return stored, nil
		case <-ctx.Done():
			return object.ObjectMeta{}, ctx.Err()
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fx.handlers.CompleteMultipartUpload(w, r, routeFor(router.OpCompleteMultipartUpload), writerAuth())
	}))
	defer server.Close()
	body := completeXML(listedSpec{1, one.ETag, ""}, listedSpec{2, two.ETag, ""})
	request, err := http.NewRequest(http.MethodPost, server.URL+"/photos/video/clip.mp4?uploadId="+testUploadID, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "application/xml" {
		t.Fatalf("status = %d content type = %q", response.StatusCode, response.Header.Get("Content-Type"))
	}
	reader := bufio.NewReader(response.Body)
	declaration, err := reader.ReadString('\n')
	if err != nil || declaration != xmlDecl {
		t.Fatalf("declaration = %q err = %v", declaration, err)
	}
	for range 3 {
		b, err := reader.ReadByte()
		if err != nil || b != ' ' {
			t.Fatalf("keepalive byte = %q err = %v", b, err)
		}
	}
	close(release)
	rest, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read rest: %v", err)
	}
	var result completeResult
	if err := xml.Unmarshal([]byte(strings.TrimLeft(string(rest), " ")), &result); err != nil {
		t.Fatalf("final document is not well-formed: %v: %q", err, rest)
	}
	if result.ETag != `"`+stored.ETag+`"` {
		t.Fatalf("ETag = %q", result.ETag)
	}
}
