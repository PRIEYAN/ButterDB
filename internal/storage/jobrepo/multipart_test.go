package jobrepo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/storage/schema"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
)

const multipartTestChannel int64 = 1001

func newMultipartTestIndex(t *testing.T) (*MultipartIndex, *sqlitedb.DB) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := sqlitedb.Open(context.Background(), sqlitedb.Control, filepath.Join(t.TempDir(), "control.db"), sqlitedb.Options{}, logger)
	if err != nil {
		t.Fatalf("open control db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := schema.Apply(context.Background(), db, schema.Control); err != nil {
		t.Fatalf("apply control schema: %v", err)
	}
	index, err := NewMultipartIndex(db)
	if err != nil {
		t.Fatalf("new multipart index: %v", err)
	}
	return index, db
}

func multipartTestID(n int) string {
	return fmt.Sprintf("%032x", n)
}

func multipartTestUpload(n int, key string) MultipartUpload {
	return MultipartUpload{
		UploadID:             multipartTestID(n),
		BucketChannelID:      multipartTestChannel,
		BucketName:           "photos",
		ObjectKey:            key,
		InitiatorAccessKeyID: "AKIATEST",
		InitiatedAt:          int64(1000 + n),
		LastActivityAt:       int64(1000 + n),
		ContentType:          "application/octet-stream",
		UserMetaJSON:         "{}",
		SystemMetaJSON:       "{}",
		State:                MultipartOpen,
	}
}

func multipartTestPart(uploadID string, number int, size int64, stagedAt int64) MultipartPart {
	return MultipartPart{UploadID: uploadID, PartNumber: number, Size: size, MD5: strings.Repeat("a", 32), StagedAt: stagedAt}
}

func multipartMustCreate(t *testing.T, index *MultipartIndex, upload MultipartUpload) {
	t.Helper()
	if err := index.CreateUpload(context.Background(), upload); err != nil {
		t.Fatalf("create upload %s: %v", upload.UploadID, err)
	}
}

func TestNewMultipartIndexRejectsNilDatabase(t *testing.T) {
	if _, err := NewMultipartIndex(nil); !errors.Is(err, errs.ErrMissingDependency) {
		t.Fatalf("err = %v, want missing dependency", err)
	}
}

func TestMultipartCreateAndGet(t *testing.T) {
	index, _ := newMultipartTestIndex(t)
	ctx := context.Background()
	want := multipartTestUpload(1, "a/b.bin")
	want.Encrypted = true
	want.ChecksumAlgorithm = "CRC32"
	want.ChecksumType = "COMPOSITE"
	want.UserMetaJSON = `{"k":"v"}`
	multipartMustCreate(t, index, want)

	got, err := index.GetUpload(ctx, want.UploadID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}

	if err := index.CreateUpload(ctx, want); !errors.Is(err, ErrDuplicateJob) {
		t.Fatalf("duplicate create err = %v, want ErrDuplicateJob", err)
	}
	if _, err := index.GetUpload(ctx, multipartTestID(99)); !errors.Is(err, errs.ErrNoSuchUpload) {
		t.Fatalf("unknown get err = %v, want no such upload", err)
	}
}

func TestMultipartPutPart(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		state   MultipartState
		wantErr error
	}{
		{name: "open accepts", state: MultipartOpen},
		{name: "committing is busy", state: MultipartCommitting, wantErr: errs.ErrUploadBusy},
		{name: "completed is gone", state: MultipartCompleted, wantErr: errs.ErrNoSuchUpload},
		{name: "aborted is gone", state: MultipartAborted, wantErr: errs.ErrNoSuchUpload},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			index, db := newMultipartTestIndex(t)
			upload := multipartTestUpload(1, "k")
			multipartMustCreate(t, index, upload)
			forceMultipartState(t, db, upload.UploadID, tc.state)
			err := index.PutPart(ctx, multipartTestPart(upload.UploadID, 1, 10, 5000))
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}

	t.Run("unknown upload", func(t *testing.T) {
		index, _ := newMultipartTestIndex(t)
		err := index.PutPart(ctx, multipartTestPart(multipartTestID(7), 1, 10, 5000))
		if !errors.Is(err, errs.ErrNoSuchUpload) {
			t.Fatalf("err = %v, want no such upload", err)
		}
	})

	t.Run("replace and activity bump", func(t *testing.T) {
		index, _ := newMultipartTestIndex(t)
		upload := multipartTestUpload(1, "k")
		multipartMustCreate(t, index, upload)
		if err := index.PutPart(ctx, multipartTestPart(upload.UploadID, 3, 10, 5000)); err != nil {
			t.Fatalf("put: %v", err)
		}
		replacement := multipartTestPart(upload.UploadID, 3, 99, 6000)
		replacement.MD5 = strings.Repeat("b", 32)
		replacement.ChecksumValue = "u1x2Pw=="
		if err := index.PutPart(ctx, replacement); err != nil {
			t.Fatalf("replace: %v", err)
		}
		parts, truncated, err := index.ListParts(ctx, upload.UploadID, 0, 10)
		if err != nil || truncated || len(parts) != 1 || parts[0] != replacement {
			t.Fatalf("parts = %+v truncated=%v err=%v", parts, truncated, err)
		}
		got, err := index.GetUpload(ctx, upload.UploadID)
		if err != nil || got.LastActivityAt != 6000 {
			t.Fatalf("last activity = %d err=%v, want 6000", got.LastActivityAt, err)
		}
	})

	t.Run("part number out of range", func(t *testing.T) {
		index, _ := newMultipartTestIndex(t)
		upload := multipartTestUpload(1, "k")
		multipartMustCreate(t, index, upload)
		if err := index.PutPart(ctx, multipartTestPart(upload.UploadID, 10001, 10, 1)); err == nil {
			t.Fatal("part 10001 accepted")
		}
	})
}

func forceMultipartState(t *testing.T, db *sqlitedb.DB, id string, state MultipartState) {
	t.Helper()
	err := db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `UPDATE multipart_uploads SET state = ?2,
  job_id = CASE WHEN ?2 = 'committing' THEN ?3 ELSE job_id END,
  put_msg_id = CASE WHEN ?2 = 'completed' THEN 5 ELSE put_msg_id END,
  part_list_sha256 = CASE WHEN ?2 = 'completed' THEN ?4 ELSE part_list_sha256 END,
  completed_at = CASE WHEN ?2 = 'completed' THEN 9 ELSE completed_at END
WHERE upload_id = ?1`, id, string(state), "00000000-0000-4000-8000-000000000000", strings.Repeat("c", 64))
		return err
	})
	if err != nil {
		t.Fatalf("force state: %v", err)
	}
}

func TestMultipartListParts(t *testing.T) {
	index, _ := newMultipartTestIndex(t)
	ctx := context.Background()
	upload := multipartTestUpload(1, "k")
	multipartMustCreate(t, index, upload)
	for _, number := range []int{5, 1, 3, 2, 4} {
		if err := index.PutPart(ctx, multipartTestPart(upload.UploadID, number, int64(number), 2000)); err != nil {
			t.Fatalf("put %d: %v", number, err)
		}
	}
	tests := []struct {
		name          string
		marker, max   int
		wantNumbers   []int
		wantTruncated bool
	}{
		{name: "first page", marker: 0, max: 2, wantNumbers: []int{1, 2}, wantTruncated: true},
		{name: "second page", marker: 2, max: 2, wantNumbers: []int{3, 4}, wantTruncated: true},
		{name: "last page", marker: 4, max: 2, wantNumbers: []int{5}},
		{name: "exact fit", marker: 3, max: 2, wantNumbers: []int{4, 5}},
		{name: "past the end", marker: 5, max: 2},
		{name: "default max", marker: 0, max: 0, wantNumbers: []int{1, 2, 3, 4, 5}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parts, truncated, err := index.ListParts(ctx, upload.UploadID, tc.marker, tc.max)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			var numbers []int
			for _, part := range parts {
				numbers = append(numbers, part.PartNumber)
			}
			if fmt.Sprint(numbers) != fmt.Sprint(tc.wantNumbers) || truncated != tc.wantTruncated {
				t.Fatalf("numbers=%v truncated=%v, want %v %v", numbers, truncated, tc.wantNumbers, tc.wantTruncated)
			}
		})
	}
	if _, _, err := index.ListParts(ctx, multipartTestID(50), 0, 10); !errors.Is(err, errs.ErrNoSuchUpload) {
		t.Fatalf("unknown upload err = %v, want no such upload", err)
	}
}

func TestMultipartListUploads(t *testing.T) {
	index, db := newMultipartTestIndex(t)
	ctx := context.Background()
	multipartMustCreate(t, index, multipartTestUpload(1, "photos/b.jpg"))
	multipartMustCreate(t, index, multipartTestUpload(2, "photos/a.jpg"))
	multipartMustCreate(t, index, multipartTestUpload(3, "photos/a.jpg"))
	multipartMustCreate(t, index, multipartTestUpload(4, "docs/x.txt"))
	done := multipartTestUpload(5, "photos/c.jpg")
	multipartMustCreate(t, index, done)
	forceMultipartState(t, db, done.UploadID, MultipartCompleted)
	other := multipartTestUpload(6, "photos/z.jpg")
	other.BucketChannelID = 2002
	multipartMustCreate(t, index, other)

	ids := func(uploads []MultipartUpload) string {
		var out []string
		for _, u := range uploads {
			out = append(out, fmt.Sprintf("%s#%d", u.ObjectKey, u.InitiatedAt-1000))
		}
		return strings.Join(out, ",")
	}
	tests := []struct {
		name          string
		query         MultipartUploadQuery
		want          string
		wantTruncated bool
	}{
		{name: "all ordered by key then time", query: MultipartUploadQuery{}, want: "docs/x.txt#4,photos/a.jpg#2,photos/a.jpg#3,photos/b.jpg#1"},
		{name: "prefix", query: MultipartUploadQuery{Prefix: "photos/a"}, want: "photos/a.jpg#2,photos/a.jpg#3"},
		{name: "key marker only", query: MultipartUploadQuery{KeyMarker: "photos/a.jpg"}, want: "photos/b.jpg#1"},
		{name: "key and upload marker", query: MultipartUploadQuery{KeyMarker: "photos/a.jpg", UploadIDMarker: multipartTestID(2)}, want: "photos/a.jpg#3,photos/b.jpg#1"},
		{name: "unknown upload marker falls back to key", query: MultipartUploadQuery{KeyMarker: "photos/a.jpg", UploadIDMarker: multipartTestID(77)}, want: "photos/b.jpg#1"},
		{name: "page truncated", query: MultipartUploadQuery{Max: 2}, want: "docs/x.txt#4,photos/a.jpg#2", wantTruncated: true},
		{name: "page exact", query: MultipartUploadQuery{Prefix: "photos/a", Max: 2}, want: "photos/a.jpg#2,photos/a.jpg#3"},
		{name: "no match", query: MultipartUploadQuery{Prefix: "none/"}, want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			uploads, truncated, err := index.ListUploads(ctx, multipartTestChannel, tc.query)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if got := ids(uploads); got != tc.want || truncated != tc.wantTruncated {
				t.Fatalf("got %q truncated=%v, want %q %v", got, truncated, tc.want, tc.wantTruncated)
			}
		})
	}
}

func TestMultipartTransition(t *testing.T) {
	ctx := context.Background()
	jobID := "11111111-1111-4111-8111-111111111111"
	sha := strings.Repeat("d", 64)
	etag := strings.Repeat("e", 32) + "-2"
	var putMsgID, completedAt int64 = 77, 123456

	t.Run("full lifecycle", func(t *testing.T) {
		index, _ := newMultipartTestIndex(t)
		upload := multipartTestUpload(1, "k")
		multipartMustCreate(t, index, upload)
		if err := index.Transition(ctx, upload.UploadID, "open", "committing", MultipartPatch{JobID: &jobID}); err != nil {
			t.Fatalf("open to committing: %v", err)
		}
		got, _ := index.GetUpload(ctx, upload.UploadID)
		if got.State != MultipartCommitting || got.JobID != jobID {
			t.Fatalf("after committing: %+v", got)
		}
		patch := MultipartPatch{ETag: &etag, PutMsgID: &putMsgID, PartListSHA256: &sha, CompletedAt: &completedAt}
		if err := index.Transition(ctx, upload.UploadID, "committing", "completed", patch); err != nil {
			t.Fatalf("committing to completed: %v", err)
		}
		got, _ = index.GetUpload(ctx, upload.UploadID)
		if got.State != MultipartCompleted || got.ETag != etag || got.PutMsgID != putMsgID || got.PartListSHA256 != sha || got.CompletedAt != completedAt || got.JobID != jobID {
			t.Fatalf("after completed: %+v", got)
		}
	})

	t.Run("committing back to open clears the job id", func(t *testing.T) {
		index, _ := newMultipartTestIndex(t)
		upload := multipartTestUpload(1, "k")
		multipartMustCreate(t, index, upload)
		if err := index.Transition(ctx, upload.UploadID, "open", "committing", MultipartPatch{JobID: &jobID}); err != nil {
			t.Fatalf("commit: %v", err)
		}
		empty := ""
		if err := index.Transition(ctx, upload.UploadID, "committing", "open", MultipartPatch{JobID: &empty}); err != nil {
			t.Fatalf("revert: %v", err)
		}
		got, _ := index.GetUpload(ctx, upload.UploadID)
		if got.State != MultipartOpen || got.JobID != "" {
			t.Fatalf("after revert: %+v", got)
		}
	})

	tests := []struct {
		name     string
		setup    MultipartState
		from, to string
		patch    MultipartPatch
		wantErr  error
	}{
		{name: "stale when state moved", setup: MultipartCommitting, from: "open", to: "aborted", wantErr: ErrStaleTransition},
		{name: "open to completed not allowed", setup: MultipartOpen, from: "open", to: "completed", wantErr: ErrInvalidTransition},
		{name: "aborted to open not allowed", setup: MultipartOpen, from: "aborted", to: "open", wantErr: ErrInvalidTransition},
		{name: "committing needs job id", setup: MultipartOpen, from: "open", to: "committing", wantErr: multipartErrCheckConstraint},
		{name: "open to aborted", setup: MultipartOpen, from: "open", to: "aborted"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			index, db := newMultipartTestIndex(t)
			upload := multipartTestUpload(1, "k")
			multipartMustCreate(t, index, upload)
			forceMultipartState(t, db, upload.UploadID, tc.setup)
			err := index.Transition(ctx, upload.UploadID, tc.from, tc.to, tc.patch)
			switch {
			case tc.wantErr == nil && err != nil:
				t.Fatalf("err = %v", err)
			case tc.wantErr == multipartErrCheckConstraint && (err == nil || !strings.Contains(err.Error(), "CHECK")):
				t.Fatalf("err = %v, want check constraint failure", err)
			case tc.wantErr != nil && tc.wantErr != multipartErrCheckConstraint && !errors.Is(err, tc.wantErr):
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}

	t.Run("unknown upload", func(t *testing.T) {
		index, _ := newMultipartTestIndex(t)
		err := index.Transition(ctx, multipartTestID(9), "open", "aborted", MultipartPatch{})
		if !errors.Is(err, errs.ErrNoSuchUpload) {
			t.Fatalf("err = %v, want no such upload", err)
		}
	})
}

var multipartErrCheckConstraint = errors.New("check constraint")

func TestMultipartDeleteRemovesParts(t *testing.T) {
	index, db := newMultipartTestIndex(t)
	ctx := context.Background()
	upload := multipartTestUpload(1, "k")
	multipartMustCreate(t, index, upload)
	if err := index.PutPart(ctx, multipartTestPart(upload.UploadID, 1, 10, 2000)); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := index.Delete(ctx, upload.UploadID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	var rows int
	err := db.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM multipart_parts) + (SELECT count(*) FROM multipart_uploads)`).Scan(&rows)
	})
	if err != nil || rows != 0 {
		t.Fatalf("rows left = %d err=%v", rows, err)
	}
	if err := index.Delete(ctx, upload.UploadID); !errors.Is(err, errs.ErrNoSuchUpload) {
		t.Fatalf("second delete err = %v, want no such upload", err)
	}
}

func TestMultipartIdleBefore(t *testing.T) {
	index, db := newMultipartTestIndex(t)
	ctx := context.Background()
	multipartMustCreate(t, index, multipartTestUpload(1, "a"))
	multipartMustCreate(t, index, multipartTestUpload(2, "b"))
	multipartMustCreate(t, index, multipartTestUpload(3, "c"))
	committing := multipartTestUpload(4, "d")
	multipartMustCreate(t, index, committing)
	forceMultipartState(t, db, committing.UploadID, MultipartCommitting)
	recent := multipartTestUpload(5, "e")
	recent.LastActivityAt = 900000
	multipartMustCreate(t, index, recent)

	cutoff := time.UnixMilli(500000)
	ids, err := index.IdleBefore(ctx, cutoff, 2)
	if err != nil {
		t.Fatalf("idle: %v", err)
	}
	if len(ids) != 2 || ids[0] != multipartTestID(1) || ids[1] != multipartTestID(2) {
		t.Fatalf("ids = %v, want first two open uploads", ids)
	}
	ids, err = index.IdleBefore(ctx, cutoff, 100)
	if err != nil || len(ids) != 3 {
		t.Fatalf("ids = %v err=%v, want three open idle uploads without the committing or recent one", ids, err)
	}
}
