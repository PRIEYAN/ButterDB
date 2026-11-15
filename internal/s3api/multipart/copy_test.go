package multipart

import (
	"encoding/xml"
	"net/http"
	"strings"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/access/sigv4"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
)

const copySourceBody = "0123456789abcdefghij"

func copySources() map[string]fakeSourceObject {
	meta := object.ObjectMeta{Key: "src/object.bin", PlaintextSize: int64(len(copySourceBody)), ETag: md5Hex([]byte(copySourceBody)), MsgDate: testNow.Unix() - 3600}
	return map[string]fakeSourceObject{"archive/src/object.bin": {meta: meta, data: []byte(copySourceBody)}}
}

func copySpec(headers map[string]string) requestSpec {
	merged := map[string]string{"X-Amz-Copy-Source": "/archive/src/object.bin"}
	for name, value := range headers {
		merged[name] = value
	}
	return requestSpec{method: http.MethodPut, target: partTarget("2"), headers: merged}
}

func TestUploadPartCopy(t *testing.T) {
	tests := []struct {
		name       string
		headers    map[string]string
		auth       func() sigv4.Auth
		wantStatus int
		wantCode   string
		wantBody   string
	}{
		{name: "whole source", wantStatus: 200, wantBody: copySourceBody},
		{name: "range inside the source", headers: map[string]string{"X-Amz-Copy-Source-Range": "bytes=5-9"}, wantStatus: 200, wantBody: "56789"},
		{name: "range to the last byte", headers: map[string]string{"X-Amz-Copy-Source-Range": "bytes=15-19"}, wantStatus: 200, wantBody: "fghij"},
		{name: "single byte range", headers: map[string]string{"X-Amz-Copy-Source-Range": "bytes=0-0"}, wantStatus: 200, wantBody: "0"},
		{name: "url encoded source without slash", headers: map[string]string{"X-Amz-Copy-Source": "archive/src%2Fobject.bin"}, wantStatus: 200, wantBody: copySourceBody},
		{name: "null version id accepted", headers: map[string]string{"X-Amz-Copy-Source": "/archive/src/object.bin?versionId=null"}, wantStatus: 200, wantBody: copySourceBody},
		{name: "range past the end", headers: map[string]string{"X-Amz-Copy-Source-Range": "bytes=10-20"}, wantStatus: 400, wantCode: "InvalidArgument"},
		{name: "range with first after last", headers: map[string]string{"X-Amz-Copy-Source-Range": "bytes=9-5"}, wantStatus: 400, wantCode: "InvalidArgument"},
		{name: "suffix range", headers: map[string]string{"X-Amz-Copy-Source-Range": "bytes=-5"}, wantStatus: 400, wantCode: "InvalidArgument"},
		{name: "open ended range", headers: map[string]string{"X-Amz-Copy-Source-Range": "bytes=5-"}, wantStatus: 400, wantCode: "InvalidArgument"},
		{name: "wrong unit", headers: map[string]string{"X-Amz-Copy-Source-Range": "items=1-2"}, wantStatus: 400, wantCode: "InvalidArgument"},
		{name: "non numeric range", headers: map[string]string{"X-Amz-Copy-Source-Range": "bytes=a-b"}, wantStatus: 400, wantCode: "InvalidArgument"},
		{name: "missing source key", headers: map[string]string{"X-Amz-Copy-Source": "/archive"}, wantStatus: 400, wantCode: "InvalidArgument"},
		{name: "other version id", headers: map[string]string{"X-Amz-Copy-Source": "/archive/src/object.bin?versionId=3"}, wantStatus: 400, wantCode: "InvalidArgument"},
		{name: "unknown source object", headers: map[string]string{"X-Amz-Copy-Source": "/archive/nothing"}, wantStatus: 404, wantCode: "NoSuchKey"},
		{name: "if-match fails", headers: map[string]string{"X-Amz-Copy-Source-If-Match": `"deadbeef"`}, wantStatus: 412, wantCode: "PreconditionFailed"},
		{name: "if-none-match hits", headers: map[string]string{"X-Amz-Copy-Source-If-None-Match": `"` + md5Hex([]byte(copySourceBody)) + `"`}, wantStatus: 412, wantCode: "PreconditionFailed"},
		{name: "if-unmodified-since before the object", headers: map[string]string{"X-Amz-Copy-Source-If-Unmodified-Since": "Thu, 01 Jan 2026 00:00:00 GMT"}, wantStatus: 412, wantCode: "PreconditionFailed"},
		{name: "if-match passes", headers: map[string]string{"X-Amz-Copy-Source-If-Match": `"` + md5Hex([]byte(copySourceBody)) + `"`}, wantStatus: 200, wantBody: copySourceBody},
		{name: "source bucket outside read scope", auth: func() sigv4.Auth { return authFor([]string{"photos"}, []string{"write"}) }, wantStatus: 403, wantCode: "AccessDenied"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fx := newFixture(t, fixtureOptions{sources: copySources()})
			caller := writerAuth()
			if test.auth != nil {
				caller = test.auth()
			}
			rec := serve(fx.handlers.UploadPartCopy, copySpec(test.headers), routeFor("UploadPartCopy"), caller)
			if test.wantCode != "" {
				assertErrorCode(t, rec, test.wantStatus, test.wantCode)
				if len(fx.stage.putCalls) != 0 {
					t.Fatalf("part was staged for a refused copy")
				}
				return
			}
			if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Body.String(), `<?xml version="1.0" encoding="UTF-8"?>`+"\n") {
				t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
			}
			var result copyPartResult
			document := rec.Body.String()
			if err := xml.Unmarshal([]byte(document[strings.Index(document, "<CopyPartResult"):]), &result); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if result.ETag != `"`+md5Hex([]byte(test.wantBody))+`"` || result.LastModified != "2026-11-13T10:05:40.000Z" {
				t.Fatalf("result = %+v", result)
			}
			call := fx.stage.putCalls[0]
			if string(call.body) != test.wantBody || call.size != int64(len(test.wantBody)) || call.number != 2 {
				t.Fatalf("put call = %+v", call)
			}
		})
	}
}

func TestUploadPartCopyFailureInBody(t *testing.T) {
	fx := newFixture(t, fixtureOptions{sources: copySources()})
	fx.stage.putErr = errs.ErrSlowDown
	rec := serve(fx.handlers.UploadPartCopy, copySpec(nil), routeFor("UploadPartCopy"), writerAuth())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want the 200 of a long response", rec.Code)
	}
	if got := decodeError(t, rec.Body.Bytes()).Code; got != "SlowDown" {
		t.Fatalf("error code in body = %q, body %s", got, rec.Body.String())
	}
	if strings.Count(rec.Body.String(), "<?xml") != 1 {
		t.Fatalf("error document carries its own declaration: %s", rec.Body.String())
	}
}

func TestUploadPartCopyEncryptedUploadHeader(t *testing.T) {
	record := openRecord()
	record.Encrypted = true
	fx := newFixture(t, fixtureOptions{sources: copySources(), records: map[string]UploadRecord{testUploadID: record}})
	rec := serve(fx.handlers.UploadPartCopy, copySpec(nil), routeFor("UploadPartCopy"), writerAuth())
	if rec.Code != http.StatusOK || rec.Header().Get(serverSideEncryption) != "AES256" {
		t.Fatalf("status = %d header = %q", rec.Code, rec.Header().Get(serverSideEncryption))
	}
}

func TestUploadPartCopyRunningTotal(t *testing.T) {
	fx := newFixture(t, fixtureOptions{sources: copySources()})
	fx.stage.setParts(partOfSize(1, 1900*1024*1024-5))
	rec := serve(fx.handlers.UploadPartCopy, copySpec(nil), routeFor("UploadPartCopy"), writerAuth())
	assertErrorCode(t, rec, http.StatusBadRequest, "EntityTooLarge")
	if len(fx.stage.putCalls) != 0 {
		t.Fatalf("part was staged past the running total")
	}
}
