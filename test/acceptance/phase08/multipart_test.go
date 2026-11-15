package phase08

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"io"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/PRIEYAN/ButterDB/internal/domain/limits"
)

func TestMultipartCompletesToOneObject(t *testing.T) {
	w := newWorld(t)
	info := w.createBucket("photos", "none")
	client := w.sdk(w.keyID, w.secret)
	ctx := context.Background()
	first := bytes.Repeat([]byte{'a'}, limits.MinMultipartPartBytes)
	second := []byte("the tail of the object")

	created, err := client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: aws.String("photos"), Key: aws.String("big/movie.bin"), ContentType: aws.String("video/mp4"),
	})
	if err != nil {
		t.Fatalf("CreateMultipartUpload: %v", err)
	}
	uploadID := created.UploadId
	var completed []types.CompletedPart
	for index, part := range [][]byte{first, second} {
		number := int32(index + 1)
		uploaded, err := client.UploadPart(ctx, &s3.UploadPartInput{
			Bucket: aws.String("photos"), Key: aws.String("big/movie.bin"), UploadId: uploadID, PartNumber: aws.Int32(number), Body: bytes.NewReader(part),
		})
		if err != nil {
			t.Fatalf("UploadPart %d: %v", number, err)
		}
		completed = append(completed, types.CompletedPart{ETag: uploaded.ETag, PartNumber: aws.Int32(number)})
	}
	if files := w.network.SentFiles(info.ChannelID); len(files) != 0 {
		t.Fatalf("staged parts sent %d documents before Complete, want none", len(files))
	}

	listed, err := client.ListParts(ctx, &s3.ListPartsInput{Bucket: aws.String("photos"), Key: aws.String("big/movie.bin"), UploadId: uploadID})
	if err != nil || len(listed.Parts) != 2 {
		t.Fatalf("ListParts = %+v, %v", listed, err)
	}
	open, err := client.ListMultipartUploads(ctx, &s3.ListMultipartUploadsInput{Bucket: aws.String("photos")})
	if err != nil || len(open.Uploads) != 1 {
		t.Fatalf("ListMultipartUploads = %+v, %v", open, err)
	}

	done, err := client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket: aws.String("photos"), Key: aws.String("big/movie.bin"), UploadId: uploadID,
		MultipartUpload: &types.CompletedMultipartUpload{Parts: completed},
	})
	if err != nil {
		t.Fatalf("CompleteMultipartUpload: %v", err)
	}
	if got := aws.ToString(done.ETag); got != multipartETag(first, second) {
		t.Fatalf("Complete ETag = %s, want %s", got, multipartETag(first, second))
	}
	if files := w.network.SentFiles(info.ChannelID); len(files) != 1 {
		t.Fatalf("Telegram holds %d documents after Complete, want exactly 1", len(files))
	}

	fetched, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("photos"), Key: aws.String("big/movie.bin")})
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	data, err := io.ReadAll(fetched.Body)
	_ = fetched.Body.Close()
	if err != nil || !bytes.Equal(data, append(append([]byte{}, first...), second...)) {
		t.Fatalf("assembled body mismatch: len %d, err %v", len(data), err)
	}
	if aws.ToString(fetched.ContentType) != "video/mp4" {
		t.Fatalf("content type = %q", aws.ToString(fetched.ContentType))
	}
	objects, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String("photos")})
	if err != nil || len(objects.Contents) != 1 || aws.ToString(objects.Contents[0].Key) != "big/movie.bin" {
		t.Fatalf("ListObjectsV2 = %+v, %v", objects, err)
	}

	_, err = client.ListParts(ctx, &s3.ListPartsInput{Bucket: aws.String("photos"), Key: aws.String("big/movie.bin"), UploadId: uploadID})
	if code := apiErrorCode(err); code != "NoSuchUpload" {
		t.Fatalf("ListParts after Complete error code = %q (%v), want NoSuchUpload", code, err)
	}
}

func multipartETag(parts ...[]byte) string {
	var digests []byte
	for _, part := range parts {
		sum := md5.Sum(part)
		digests = append(digests, sum[:]...)
	}
	final := md5.Sum(digests)
	return `"` + hex.EncodeToString(final[:]) + "-" + string(rune('0'+len(parts))) + `"`
}
