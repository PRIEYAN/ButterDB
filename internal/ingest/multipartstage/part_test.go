package multipartstage

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/crypto/format"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/limits"
	"github.com/PRIEYAN/ButterDB/internal/storage/jobrepo"
)

type explodingReader struct {
	t *testing.T
}

func (r explodingReader) Read([]byte) (int, error) {
	r.t.Helper()
	r.t.Fatal("part body was read")
	return 0, io.EOF
}

type failingReader struct {
	prefix []byte
	err    error
	sent   bool
}

func (r *failingReader) Read(p []byte) (int, error) {
	if r.sent {
		return 0, r.err
	}
	r.sent = true
	return copy(p, r.prefix), nil
}

func TestPartMD5ETag(t *testing.T) {
	h := newHarness(t)
	upload := h.create(t, "k", false)
	tests := []struct {
		name string
		data []byte
	}{
		{name: "small", data: []byte("hello multipart")},
		{name: "empty", data: nil},
		{name: "one mebibyte", data: filled(1<<20, 'z')},
	}
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			part := h.putPart(t, upload.ID, i+1, tc.data)
			sum := md5.Sum(tc.data)
			if want := hex.EncodeToString(sum[:]); part.ETag != want {
				t.Fatalf("etag = %s, want %s", part.ETag, want)
			}
			if part.Size != int64(len(tc.data)) || part.Number != i+1 || !part.StagedAt.Equal(testStart) {
				t.Fatalf("part = %+v", part)
			}
			stored, err := os.ReadFile(h.partFile(upload.ID, i+1))
			if err != nil || !bytes.Equal(stored, tc.data) {
				t.Fatalf("stored bytes differ: err=%v", err)
			}
		})
	}
}

func TestPartFileModes(t *testing.T) {
	h := newHarness(t)
	upload := h.create(t, "k", false)
	h.putPart(t, upload.ID, 7, []byte("x"))
	fileInfo, err := os.Stat(h.partFile(upload.ID, 7))
	if err != nil || fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("part file mode = %v err=%v, want 0600", fileInfo.Mode().Perm(), err)
	}
	dirInfo, err := os.Stat(h.stage.uploadDir(upload.ID))
	if err != nil || dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("upload dir mode = %v err=%v, want 0700", dirInfo.Mode().Perm(), err)
	}
	if h.partFile(upload.ID, 7) != h.stage.uploadDir(upload.ID)+"/part-00007" {
		t.Fatalf("part path = %s", h.partFile(upload.ID, 7))
	}
}

func TestPartReplaceAtomic(t *testing.T) {
	h := newHarness(t)
	upload := h.create(t, "k", false)
	original := []byte("original bytes")
	h.putPart(t, upload.ID, 1, original)

	t.Run("failed replacement keeps the earlier file", func(t *testing.T) {
		broken := &failingReader{prefix: []byte("partial"), err: errors.New("connection reset")}
		if _, err := h.stage.PutPart(context.Background(), upload.ID, 1, 64, broken); err == nil {
			t.Fatal("broken body accepted")
		}
		stored, err := os.ReadFile(h.partFile(upload.ID, 1))
		if err != nil || !bytes.Equal(stored, original) {
			t.Fatalf("stored = %q err=%v, want the original", stored, err)
		}
		entries, err := h.fs.ReadDir(h.stage.uploadDir(upload.ID))
		if err != nil || len(entries) != 1 {
			t.Fatalf("entries = %v err=%v, want only the part file", entries, err)
		}
	})

	t.Run("canceled replacement keeps the earlier file", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := h.stage.PutPart(ctx, upload.ID, 1, 5, bytes.NewReader([]byte("12345"))); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want canceled", err)
		}
		stored, _ := os.ReadFile(h.partFile(upload.ID, 1))
		if !bytes.Equal(stored, original) {
			t.Fatalf("stored = %q, want the original", stored)
		}
	})

	t.Run("successful replacement swaps file and row", func(t *testing.T) {
		replacement := []byte("replacement")
		part := h.putPart(t, upload.ID, 1, replacement)
		stored, err := os.ReadFile(h.partFile(upload.ID, 1))
		if err != nil || !bytes.Equal(stored, replacement) {
			t.Fatalf("stored = %q err=%v, want the replacement", stored, err)
		}
		page, err := h.stage.ListParts(context.Background(), upload.ID, 0, 10)
		if err != nil || len(page.Parts) != 1 || page.Parts[0].ETag != part.ETag || page.Parts[0].Size != int64(len(replacement)) {
			t.Fatalf("page = %+v err=%v", page, err)
		}
		entries, _ := h.fs.ReadDir(h.stage.uploadDir(upload.ID))
		if len(entries) != 1 {
			t.Fatalf("entries = %v, want only the part file", entries)
		}
	})
}

func seedPartRow(t *testing.T, h *harness, uploadID string, number int, size int64) {
	t.Helper()
	row := jobrepo.MultipartPart{UploadID: uploadID, PartNumber: number, Size: size, MD5: hex.EncodeToString(make([]byte, 16)), StagedAt: 1}
	if err := h.real.PutPart(context.Background(), row); err != nil {
		t.Fatalf("seed part %d: %v", number, err)
	}
}

func TestRunningTotalCheckedBeforeBody(t *testing.T) {
	h := newHarness(t)
	upload := h.create(t, "k", false)
	seedPartRow(t, h, upload.ID, 1, limits.MaxStoredObjectBytes-10)
	ctx := context.Background()

	if _, err := h.stage.PutPart(ctx, upload.ID, 2, 11, explodingReader{t: t}); !errors.Is(err, errs.ErrObjectTooLarge) {
		t.Fatalf("over the ceiling err = %v, want object too large", err)
	}
	if _, err := h.stage.PutPart(ctx, upload.ID, 3, limits.MaxStoredObjectBytes+1, explodingReader{t: t}); !errors.Is(err, errs.ErrObjectTooLarge) {
		t.Fatalf("single part over the ceiling err = %v, want object too large", err)
	}
	if _, err := h.stage.PutPart(ctx, upload.ID, 2, 10, bytes.NewReader(filled(10, 'a'))); err != nil {
		t.Fatalf("part that exactly fits: %v", err)
	}
	if _, err := h.stage.PutPart(ctx, upload.ID, 2, 11, explodingReader{t: t}); !errors.Is(err, errs.ErrObjectTooLarge) {
		t.Fatalf("growing a part past the ceiling err = %v", err)
	}
	if _, err := h.stage.PutPart(ctx, upload.ID, 1, limits.MaxStoredObjectBytes-10, bytes.NewReader(nil)); err == nil {
		t.Fatal("body shorter than declared accepted")
	}
}

func TestEncryptedTotalUsesCiphertextSize(t *testing.T) {
	h := newHarness(t)
	plaintextSize := int64(limits.MaxStoredObjectBytes - 100)
	if format.CiphertextSize(plaintextSize+1) <= limits.MaxStoredObjectBytes {
		t.Fatal("test sizes do not cross the ceiling once encrypted")
	}
	ctx := context.Background()

	encrypted := h.create(t, "enc", true)
	seedPartRow(t, h, encrypted.ID, 1, plaintextSize)
	if _, err := h.stage.PutPart(ctx, encrypted.ID, 2, 1, explodingReader{t: t}); !errors.Is(err, errs.ErrObjectTooLarge) {
		t.Fatalf("encrypted err = %v, want object too large", err)
	}

	plain := h.create(t, "plain", false)
	seedPartRow(t, h, plain.ID, 1, plaintextSize)
	if _, err := h.stage.PutPart(ctx, plain.ID, 2, 1, bytes.NewReader([]byte("x"))); err != nil {
		t.Fatalf("unencrypted part under the ceiling: %v", err)
	}
}
