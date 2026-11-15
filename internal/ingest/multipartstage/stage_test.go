package multipartstage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/storage/jobrepo"
)

func TestNewRejectsMissingDependencies(t *testing.T) {
	base := newHarness(t)
	full := Dependencies{Index: base.index, Putter: base.putter, Dir: base.dir, Clock: clock.NewFake(testStart), Random: &sequenceRandom{}, FS: base.fs}
	tests := []struct {
		name   string
		mutate func(d *Dependencies)
	}{
		{name: "no index", mutate: func(d *Dependencies) { d.Index = nil }},
		{name: "no putter", mutate: func(d *Dependencies) { d.Putter = nil }},
		{name: "no dir", mutate: func(d *Dependencies) { d.Dir = "" }},
		{name: "no clock", mutate: func(d *Dependencies) { d.Clock = nil }},
		{name: "no random", mutate: func(d *Dependencies) { d.Random = nil }},
		{name: "no fs", mutate: func(d *Dependencies) { d.FS = nil }},
		{name: "fs that cannot open files", mutate: func(d *Dependencies) { d.FS = base.fs.Rooted }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deps := full
			tc.mutate(&deps)
			if _, err := New(deps); !errors.Is(err, errs.ErrMissingDependency) {
				t.Fatalf("err = %v, want missing dependency", err)
			}
		})
	}
	if _, err := New(full); err != nil {
		t.Fatalf("full dependencies rejected: %v", err)
	}
}

func TestCreateRecordsUpload(t *testing.T) {
	h := newHarness(t)
	upload := h.create(t, "video/clip.mp4", true)
	if len(upload.ID) != 32 || validateUploadID(upload.ID) != nil {
		t.Fatalf("upload id %q is not 32 lowercase hex", upload.ID)
	}
	if upload.State != "open" || upload.Key != "video/clip.mp4" || upload.Bucket != "photos" || !upload.InitiatedAt.Equal(testStart) {
		t.Fatalf("upload = %+v", upload)
	}
	row, err := h.real.GetUpload(context.Background(), upload.ID)
	if err != nil {
		t.Fatalf("get row: %v", err)
	}
	if !row.Encrypted || row.InitiatorAccessKeyID != "AKIATEST" || row.UserMetaJSON != `{"owner":"alice"}` || row.ContentType != "video/mp4" {
		t.Fatalf("row = %+v", row)
	}
	second := h.create(t, "video/clip.mp4", false)
	if second.ID == upload.ID {
		t.Fatal("two uploads share an id")
	}
}

func TestCreateRejectsInvalidRequests(t *testing.T) {
	h := newHarness(t)
	tests := []struct {
		name string
		req  CreateRequest
	}{
		{name: "empty key", req: CreateRequest{Bucket: "photos", BucketChannelID: testChannel, ContentType: "a/b"}},
		{name: "no channel", req: CreateRequest{Bucket: "photos", Key: "k", ContentType: "a/b"}},
		{name: "no content type", req: CreateRequest{Bucket: "photos", BucketChannelID: testChannel, Key: "k"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := h.stage.Create(context.Background(), tc.req); !errors.Is(err, errs.ErrInvalidKey) {
				t.Fatalf("err = %v, want invalid key", err)
			}
		})
	}
}

func TestUploadIDValidatedBeforePath(t *testing.T) {
	h := newHarness(t)
	crafted := []string{
		"",
		"../../etc/passwd",
		"..",
		strings.Repeat("a", 31),
		strings.Repeat("a", 33),
		strings.Repeat("A", 32),
		strings.Repeat("g", 32),
		"../" + strings.Repeat("a", 29),
		strings.Repeat("a", 31) + "/",
		strings.Repeat("a", 30) + `\x`,
		strings.Repeat("a", 31) + "\x00",
	}
	for _, id := range crafted {
		t.Run(fmt.Sprintf("%q", id), func(t *testing.T) {
			before := h.fs.callCount()
			ctx := context.Background()
			_, putErr := h.stage.PutPart(ctx, id, 1, 1, strings.NewReader("x"))
			_, listErr := h.stage.ListParts(ctx, id, 0, 10)
			_, completeErr := h.stage.Complete(ctx, id, []CompletedPart{{Number: 1, ETag: "x"}}, Conditions{})
			abortErr := h.stage.Abort(ctx, id)
			for name, err := range map[string]error{"put part": putErr, "list parts": listErr, "complete": completeErr, "abort": abortErr} {
				if !errors.Is(err, errs.ErrNoSuchUpload) {
					t.Fatalf("%s err = %v, want no such upload", name, err)
				}
			}
			if after := h.fs.callCount(); after != before {
				t.Fatalf("filesystem touched %d times for a crafted id", after-before)
			}
		})
	}
}

func TestListPartsPagesAndMarkers(t *testing.T) {
	h := newHarness(t)
	upload := h.create(t, "k", false)
	for _, number := range []int{3, 1, 2} {
		h.putPart(t, upload.ID, number, filled(number, 'x'))
	}
	page, err := h.stage.ListParts(context.Background(), upload.ID, 0, 2)
	if err != nil || !page.Truncated || page.NextMarker != 2 || len(page.Parts) != 2 || page.Parts[0].Number != 1 {
		t.Fatalf("first page = %+v err=%v", page, err)
	}
	page, err = h.stage.ListParts(context.Background(), upload.ID, page.NextMarker, 2)
	if err != nil || page.Truncated || page.NextMarker != 0 || len(page.Parts) != 1 || page.Parts[0].Number != 3 || page.Parts[0].Size != 3 {
		t.Fatalf("second page = %+v err=%v", page, err)
	}
}

func TestListUploadsDelimiterAndPaging(t *testing.T) {
	h := newHarness(t)
	for _, key := range []string{"docs/a.txt", "docs/b.txt", "photos/2026/x.jpg", "photos/2026/y.jpg", "photos/top.jpg", "readme"} {
		h.create(t, key, false)
	}
	ctx := context.Background()
	page, err := h.stage.ListUploads(ctx, testChannel, UploadQuery{Delimiter: "/"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if fmt.Sprint(page.CommonPrefixes) != "[docs/ photos/]" || len(page.Uploads) != 1 || page.Uploads[0].Key != "readme" || page.Truncated {
		t.Fatalf("page = %+v", page)
	}

	page, err = h.stage.ListUploads(ctx, testChannel, UploadQuery{Prefix: "photos/", Delimiter: "/"})
	if err != nil || fmt.Sprint(page.CommonPrefixes) != "[photos/2026/]" || len(page.Uploads) != 1 || page.Uploads[0].Key != "photos/top.jpg" {
		t.Fatalf("photos page = %+v err=%v", page, err)
	}

	var keys []string
	query := UploadQuery{Max: 2}
	for rounds := 0; rounds < 10; rounds++ {
		page, err = h.stage.ListUploads(ctx, testChannel, query)
		if err != nil {
			t.Fatalf("paged list: %v", err)
		}
		for _, upload := range page.Uploads {
			keys = append(keys, string(upload.Key))
		}
		if !page.Truncated {
			break
		}
		query.KeyMarker, query.UploadIDMarker = page.NextKeyMarker, page.NextUploadIDMarker
	}
	if len(keys) != 6 {
		t.Fatalf("paged keys = %v, want all six", keys)
	}

	query = UploadQuery{Delimiter: "/", Max: 1}
	var seen []string
	for rounds := 0; rounds < 10; rounds++ {
		page, err = h.stage.ListUploads(ctx, testChannel, query)
		if err != nil {
			t.Fatalf("delimited paged list: %v", err)
		}
		seen = append(seen, page.CommonPrefixes...)
		for _, upload := range page.Uploads {
			seen = append(seen, string(upload.Key))
		}
		if !page.Truncated {
			break
		}
		query.KeyMarker, query.UploadIDMarker = page.NextKeyMarker, page.NextUploadIDMarker
	}
	if fmt.Sprint(seen) != "[docs/ photos/ readme]" {
		t.Fatalf("delimited paging saw %v", seen)
	}
}

func TestPutPartRejectsStatesAndNumbers(t *testing.T) {
	h := newHarness(t)
	upload := h.create(t, "k", false)
	ctx := context.Background()
	for _, number := range []int{0, -1, 10001} {
		if _, err := h.stage.PutPart(ctx, upload.ID, number, 1, strings.NewReader("x")); !errors.Is(err, errs.ErrInvalidPart) {
			t.Fatalf("part %d err = %v, want invalid part", number, err)
		}
	}
	if _, err := h.stage.PutPart(ctx, strings.Repeat("0", 32), 1, 1, strings.NewReader("x")); !errors.Is(err, errs.ErrNoSuchUpload) {
		t.Fatalf("unknown upload err = %v", err)
	}
	jobID := "11111111-1111-4111-8111-111111111111"
	if err := h.real.Transition(ctx, upload.ID, "open", "committing", jobrepo.MultipartPatch{JobID: &jobID}); err != nil {
		t.Fatalf("transition: %v", err)
	}
	if _, err := h.stage.PutPart(ctx, upload.ID, 1, 1, strings.NewReader("x")); !errors.Is(err, errs.ErrUploadBusy) {
		t.Fatalf("committing err = %v, want upload busy", err)
	}
}

func TestPutPartRejectsWrongLength(t *testing.T) {
	h := newHarness(t)
	upload := h.create(t, "k", false)
	ctx := context.Background()
	if _, err := h.stage.PutPart(ctx, upload.ID, 1, 10, strings.NewReader("short")); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("short body err = %v, want unexpected EOF", err)
	}
	if _, err := h.stage.PutPart(ctx, upload.ID, 1, 3, strings.NewReader("too long")); !errors.Is(err, errs.ErrInvalidPart) {
		t.Fatalf("long body err = %v, want invalid part", err)
	}
	page, err := h.stage.ListParts(ctx, upload.ID, 0, 10)
	if err != nil || len(page.Parts) != 0 {
		t.Fatalf("parts after failures = %+v err=%v", page, err)
	}
	entries, err := h.fs.ReadDir(h.stage.uploadDir(upload.ID))
	if err != nil || len(entries) != 0 {
		t.Fatalf("entries after failures = %v err=%v", entries, err)
	}
}

var _ Index = (*jobrepo.MultipartIndex)(nil)
