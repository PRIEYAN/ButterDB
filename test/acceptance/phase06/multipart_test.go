package phase06

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/domain/limits"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/ingest/multipartstage"
)

func TestMultipartPartsAssembleToOneDocument(t *testing.T) {
	w := newWorld(t)
	info := w.createBucket("photos", "none")
	ctx := context.Background()
	stage := w.write.Stage

	upload, err := stage.Create(ctx, multipartstage.CreateRequest{
		Bucket: "photos", BucketChannelID: info.ChannelID, Key: object.Key("big/movie.bin"), ContentType: "video/mp4", AccessKeyID: accessKeyID,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	first := bytes.Repeat([]byte{'a'}, limits.MinMultipartPartBytes)
	second := []byte("the tail of the object")
	stalePart := bytes.Repeat([]byte{'z'}, limits.MinMultipartPartBytes)
	staged := make([]multipartstage.Part, 0, 3)
	for number, body := range map[int][]byte{1: first, 2: second, 3: stalePart} {
		part, err := stage.PutPart(ctx, upload.ID, number, int64(len(body)), bytes.NewReader(body))
		if err != nil {
			t.Fatalf("PutPart %d: %v", number, err)
		}
		staged = append(staged, part)
	}
	if files := w.network.SentFiles(info.ChannelID); len(files) != 0 {
		t.Fatalf("staging parts sent %d documents to Telegram, want none before Complete", len(files))
	}
	partFiles, err := os.ReadDir(filepath.Join(w.dir, "multipart", upload.ID))
	if err != nil || len(partFiles) != 3 {
		t.Fatalf("staged part files = %v, %v; want 3", partFiles, err)
	}

	etags := map[int]string{}
	for _, part := range staged {
		etags[part.Number] = part.ETag
	}
	meta, err := stage.Complete(ctx, upload.ID, []multipartstage.CompletedPart{
		{Number: 1, ETag: etags[1]}, {Number: 2, ETag: etags[2]},
	}, multipartstage.Conditions{})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	files := w.network.SentFiles(info.ChannelID)
	total := int64(len(first) + len(second))
	if len(files) != 1 || files[0].Size != total || files[0].MsgID != meta.BodyMsgID {
		t.Fatalf("sent files = %+v, want exactly one document of %d bytes", files, total)
	}
	want := append(append([]byte(nil), first...), second...)
	if got := w.documentBytes(info.ChannelID, meta.BodyMsgID, total); !bytes.Equal(got, want) {
		t.Fatal("the assembled document is not the listed parts concatenated in order")
	}
	if wantETag := multipartETag(first, second); meta.ETag != wantETag {
		t.Fatalf("object ETag = %q, want %q", meta.ETag, wantETag)
	}
	if got := w.projected(`SELECT put_msg_id, stored_size FROM objects WHERE channel_id = ?1 AND object_key = 'big/movie.bin'`, info.ChannelID); len(got) != 1 || got[0] != itoa(meta.PutMsgID)+"|"+itoa(total) {
		t.Fatalf("object row = %v", got)
	}
	if got := w.controlRows(`SELECT state, etag FROM multipart_uploads WHERE upload_id = ?1`, upload.ID); len(got) != 1 || got[0] != "completed|"+meta.ETag {
		t.Fatalf("multipart upload row = %v", got)
	}
	if _, err := os.Stat(filepath.Join(w.dir, "multipart", upload.ID)); !os.IsNotExist(err) {
		t.Fatalf("staged part directory after Complete: %v, want removed", err)
	}
}

func multipartETag(parts ...[]byte) string {
	var sums []byte
	for _, part := range parts {
		sum := md5.Sum(part)
		sums = append(sums, sum[:]...)
	}
	final := md5.Sum(sums)
	return fmt.Sprintf("%s-%d", hex.EncodeToString(final[:]), len(parts))
}
