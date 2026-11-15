package multipart

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/ingest/multipartstage"
	"github.com/PRIEYAN/ButterDB/internal/s3api/router"
)

func listPartsSpec(query string) requestSpec {
	return requestSpec{method: http.MethodGet, target: "/photos/video/clip.mp4?uploadId=" + testUploadID + query}
}

func serveListParts(fx *fixture, query string) *httptest.ResponseRecorder {
	return serve(fx.handlers.ListParts, listPartsSpec(query), routeFor(router.OpListParts), writerAuth())
}

func TestListPartsResult(t *testing.T) {
	record := openRecord()
	record.ChecksumAlgorithm = "crc32"
	record.ChecksumType = "COMPOSITE"
	fx := newFixture(t, fixtureOptions{records: map[string]UploadRecord{testUploadID: record}})
	staged := multipartstage.Part{Number: 3, Size: 12, ETag: etagOf("three"), Checksum: "u1x2Pw==", StagedAt: testNow}
	fx.stage.listPagesFn = func(marker, max int) (multipartstage.PartPage, error) {
		return multipartstage.PartPage{Parts: []multipartstage.Part{staged}, NextMarker: 3, Truncated: true}, nil
	}
	rec := serveListParts(fx, "&max-parts=1&part-number-marker=2")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	result := decodeResult[listPartsResult](t, rec.Body.Bytes(), "ListPartsResult")
	if result.Bucket != testBucket || result.Key != testKey || result.UploadID != testUploadID {
		t.Fatalf("identity = %+v", result)
	}
	if result.PartNumberMarker != 2 || result.NextPartNumberMarker != 3 || result.MaxParts != 1 || !result.IsTruncated {
		t.Fatalf("paging = %+v", result)
	}
	if result.StorageClass != "STANDARD" || result.Owner.ID != testOwnerID || result.Owner.DisplayName != "butterdb" {
		t.Fatalf("owner = %+v storage class = %q", result.Owner, result.StorageClass)
	}
	if result.Initiator.ID != testAccessKey || result.Initiator.DisplayName != "uploader" {
		t.Fatalf("initiator = %+v", result.Initiator)
	}
	if result.ChecksumAlgorithm != "CRC32" || result.ChecksumType != "COMPOSITE" {
		t.Fatalf("checksum = %q %q", result.ChecksumAlgorithm, result.ChecksumType)
	}
	if len(result.Parts) != 1 {
		t.Fatalf("parts = %+v", result.Parts)
	}
	part := result.Parts[0]
	if part.PartNumber != 3 || part.Size != 12 || part.ETag != `"`+staged.ETag+`"` || part.ChecksumCRC32 != "u1x2Pw==" || part.LastModified != "2026-11-13T10:05:40.000Z" {
		t.Fatalf("part = %+v", part)
	}
	if got := fx.stage.listPartsArgs[len(fx.stage.listPartsArgs)-1]; got != [2]int{2, 1} {
		t.Fatalf("stage arguments = %v", got)
	}
}

func TestListPartsParameters(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		wantMax    int
		wantMarker int
		wantCode   string
	}{
		{name: "defaults", wantMax: 1000},
		{name: "explicit limit", query: "&max-parts=5", wantMax: 5},
		{name: "limit clamped to one thousand", query: "&max-parts=5000", wantMax: 1000},
		{name: "limit zero", query: "&max-parts=0", wantCode: "InvalidArgument"},
		{name: "negative limit", query: "&max-parts=-1", wantCode: "InvalidArgument"},
		{name: "non numeric limit", query: "&max-parts=many", wantCode: "InvalidArgument"},
		{name: "marker", query: "&part-number-marker=7", wantMax: 1000, wantMarker: 7},
		{name: "negative marker", query: "&part-number-marker=-1", wantCode: "InvalidArgument"},
		{name: "non numeric marker", query: "&part-number-marker=x", wantCode: "InvalidArgument"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fx := newFixture(t, fixtureOptions{})
			rec := serveListParts(fx, test.query)
			if test.wantCode != "" {
				parsed := assertErrorCode(t, rec, http.StatusBadRequest, test.wantCode)
				if parsed.field("ArgumentName") == "" {
					t.Fatalf("argument name missing: %+v", parsed)
				}
				return
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
			}
			if got := fx.stage.listPartsArgs[0]; got != [2]int{test.wantMarker, test.wantMax} {
				t.Fatalf("stage arguments = %v, want marker %d max %d", got, test.wantMarker, test.wantMax)
			}
			if result := decodeResult[listPartsResult](t, rec.Body.Bytes(), "ListPartsResult"); result.MaxParts != test.wantMax || result.NextPartNumberMarker != 0 || result.IsTruncated {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}

func TestListPartsUploadStates(t *testing.T) {
	committing := openRecord()
	committing.State = stateCommitting
	completed := openRecord()
	completed.State = stateCompleted
	aborted := openRecord()
	aborted.State = stateAborted
	elsewhere := openRecord()
	elsewhere.Key = "other/key"
	tests := []struct {
		name   string
		record UploadRecord
		wantOK bool
	}{
		{name: "open", record: openRecord(), wantOK: true},
		{name: "committing", record: committing, wantOK: true},
		{name: "completed", record: completed},
		{name: "aborted", record: aborted},
		{name: "another key", record: elsewhere},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fx := newFixture(t, fixtureOptions{records: map[string]UploadRecord{testUploadID: test.record}})
			rec := serveListParts(fx, "")
			if test.wantOK {
				if rec.Code != http.StatusOK {
					t.Fatalf("status = %d", rec.Code)
				}
				return
			}
			assertErrorCode(t, rec, http.StatusNotFound, "NoSuchUpload")
		})
	}
}

func TestListPartsStageFailure(t *testing.T) {
	fx := newFixture(t, fixtureOptions{})
	fx.stage.listPagesFn = func(int, int) (multipartstage.PartPage, error) {
		return multipartstage.PartPage{}, errs.ErrNoSuchUpload
	}
	assertErrorCode(t, serveListParts(fx, ""), http.StatusNotFound, "NoSuchUpload")
}

func serveListUploads(fx *fixture, query string) *httptest.ResponseRecorder {
	spec := requestSpec{method: http.MethodGet, target: "/photos?uploads" + query}
	return serve(fx.handlers.ListMultipartUploads, spec, bucketRoute(router.OpListMultipartUploads), writerAuth())
}

func TestListMultipartUploads(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		wantQuery multipartstage.UploadQuery
		wantCode  string
	}{
		{name: "defaults", wantQuery: multipartstage.UploadQuery{Max: 1000}},
		{name: "all parameters", query: "&prefix=video/&delimiter=/&key-marker=video/a&upload-id-marker=abc&max-uploads=7", wantQuery: multipartstage.UploadQuery{Prefix: "video/", Delimiter: "/", KeyMarker: "video/a", UploadIDMarker: "abc", Max: 7}},
		{name: "upload id marker without key marker is ignored", query: "&upload-id-marker=abc", wantQuery: multipartstage.UploadQuery{Max: 1000}},
		{name: "limit clamped", query: "&max-uploads=99999", wantQuery: multipartstage.UploadQuery{Max: 1000}},
		{name: "limit zero", query: "&max-uploads=0", wantCode: "InvalidArgument"},
		{name: "limit not a number", query: "&max-uploads=x", wantCode: "InvalidArgument"},
		{name: "unknown encoding type", query: "&encoding-type=base64", wantCode: "InvalidArgument"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fx := newFixture(t, fixtureOptions{})
			rec := serveListUploads(fx, test.query)
			if test.wantCode != "" {
				assertErrorCode(t, rec, http.StatusBadRequest, test.wantCode)
				return
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
			}
			if fx.stage.uploadsQuery != test.wantQuery || fx.stage.uploadsBucket != testChannelID {
				t.Fatalf("stage query = %+v channel %d, want %+v channel %d", fx.stage.uploadsQuery, fx.stage.uploadsBucket, test.wantQuery, testChannelID)
			}
		})
	}
}

func TestListMultipartUploadsResult(t *testing.T) {
	initiated := testNow.Add(-time.Hour)
	fx := newFixture(t, fixtureOptions{})
	fx.stage.uploadsPage = multipartstage.UploadPage{
		Uploads: []multipartstage.Upload{
			{ID: testUploadID, Bucket: testBucket, Key: "video/a b.mp4", InitiatedAt: initiated, State: stateOpen},
		},
		CommonPrefixes:     []string{"video/sub/"},
		NextKeyMarker:      "video/a b.mp4",
		NextUploadIDMarker: testUploadID,
		Truncated:          true,
	}
	rec := serveListUploads(fx, "&prefix=video/&delimiter=/&max-uploads=2")
	result := decodeResult[listUploadsResult](t, rec.Body.Bytes(), "ListMultipartUploadsResult")
	if result.Bucket != testBucket || result.Prefix != "video/" || result.Delimiter != "/" || result.MaxUploads != 2 || !result.IsTruncated {
		t.Fatalf("result = %+v", result)
	}
	if result.NextKeyMarker != "video/a b.mp4" || result.NextUploadIDMarker != testUploadID || result.EncodingType != "" {
		t.Fatalf("markers = %+v", result)
	}
	if len(result.Uploads) != 1 || result.Uploads[0].Key != "video/a b.mp4" || result.Uploads[0].UploadID != testUploadID || result.Uploads[0].Initiated != "2026-11-13T09:05:40.000Z" {
		t.Fatalf("uploads = %+v", result.Uploads)
	}
	if upload := result.Uploads[0]; upload.StorageClass != "STANDARD" || upload.Owner.ID != testOwnerID || upload.Initiator.ID != testOwnerID {
		t.Fatalf("upload identity = %+v", upload)
	}
	if len(result.CommonPrefixes) != 1 || result.CommonPrefixes[0].Prefix != "video/sub/" {
		t.Fatalf("common prefixes = %+v", result.CommonPrefixes)
	}
}

func TestListMultipartUploadsEncodingAndEmpty(t *testing.T) {
	fx := newFixture(t, fixtureOptions{})
	fx.stage.uploadsPage = multipartstage.UploadPage{
		Uploads:        []multipartstage.Upload{{ID: testUploadID, Key: "dir/a b+c.txt", InitiatedAt: testNow}},
		CommonPrefixes: []string{"dir/é/"},
	}
	rec := serveListUploads(fx, "&encoding-type=url&prefix=dir%2F")
	result := decodeResult[listUploadsResult](t, rec.Body.Bytes(), "ListMultipartUploadsResult")
	if result.EncodingType != "url" || result.Prefix != "dir/" || result.Uploads[0].Key != "dir/a%20b%2Bc.txt" || result.CommonPrefixes[0].Prefix != "dir/%C3%A9/" {
		t.Fatalf("result = %+v", result)
	}
	if result.NextKeyMarker != "" || result.IsTruncated {
		t.Fatalf("untruncated result carries markers: %+v", result)
	}
	empty := newFixture(t, fixtureOptions{})
	body := serveListUploads(empty, "").Body.String()
	if strings.Contains(body, "<Upload>") || strings.Contains(body, "<CommonPrefixes>") || !strings.Contains(body, "<IsTruncated>false</IsTruncated>") {
		t.Fatalf("empty listing = %s", body)
	}
}

func TestListMultipartUploadsAccess(t *testing.T) {
	tests := []struct {
		name string
		call func(fx *fixture) *httptest.ResponseRecorder
	}{
		{name: "key without list action", call: func(fx *fixture) *httptest.ResponseRecorder {
			spec := requestSpec{method: http.MethodGet, target: "/photos?uploads"}
			return serve(fx.handlers.ListMultipartUploads, spec, bucketRoute(router.OpListMultipartUploads), authFor([]string{"*"}, []string{"write"}))
		}},
		{name: "bucket outside scope", call: func(fx *fixture) *httptest.ResponseRecorder {
			spec := requestSpec{method: http.MethodGet, target: "/photos?uploads"}
			return serve(fx.handlers.ListMultipartUploads, spec, bucketRoute(router.OpListMultipartUploads), authFor([]string{"archive"}, []string{"list"}))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fx := newFixture(t, fixtureOptions{})
			assertErrorCode(t, test.call(fx), http.StatusForbidden, "AccessDenied")
		})
	}
}

func TestRoutesCoverEveryMultipartOperation(t *testing.T) {
	fx := newFixture(t, fixtureOptions{})
	routes := fx.handlers.Routes()
	for _, op := range []router.Operation{
		router.OpCreateMultipartUpload, router.OpUploadPart, router.OpUploadPartCopy, router.OpCompleteMultipartUpload,
		router.OpAbortMultipartUpload, router.OpListParts, router.OpListMultipartUploads,
	} {
		if routes[op] == nil {
			t.Fatalf("no handler registered for %s", op)
		}
	}
	if len(routes) != 7 {
		t.Fatalf("routes = %d, want 7", len(routes))
	}
}
