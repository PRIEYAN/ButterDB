package phase08

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"io"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

func TestAwsSdkGoV2CanPUTGETHEADLISTAndDELETE(t *testing.T) {
	tests := []struct {
		name       string
		encryption string
	}{
		{name: "plaintext bucket", encryption: "none"},
		{name: "encrypted bucket", encryption: "tde1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newWorld(t)
			w.createBucket("photos", tt.encryption)
			client := w.sdk(w.keyID, w.secret)
			ctx := context.Background()
			body := randomBytes("sdk crud "+tt.name, 300_000)
			sum := md5.Sum(body)
			wantETag := `"` + hex.EncodeToString(sum[:]) + `"`

			put, err := client.PutObject(ctx, &s3.PutObjectInput{
				Bucket: aws.String("photos"), Key: aws.String("albums/2025/cat.jpg"), Body: bytes.NewReader(body),
				ContentType: aws.String("image/jpeg"), Metadata: map[string]string{"shot": "morning"},
			})
			if err != nil {
				t.Fatalf("PutObject: %v", err)
			}
			if aws.ToString(put.ETag) != wantETag {
				t.Fatalf("PutObject ETag = %s, want %s", aws.ToString(put.ETag), wantETag)
			}
			if _, err := client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String("photos"), Key: aws.String("albums/readme.txt"), Body: bytes.NewReader([]byte("hello"))}); err != nil {
				t.Fatalf("PutObject readme: %v", err)
			}

			got, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("photos"), Key: aws.String("albums/2025/cat.jpg")})
			if err != nil {
				t.Fatalf("GetObject: %v", err)
			}
			data, err := io.ReadAll(got.Body)
			_ = got.Body.Close()
			if err != nil || !bytes.Equal(data, body) {
				t.Fatalf("GetObject body mismatch: len %d, err %v", len(data), err)
			}
			if aws.ToString(got.ContentType) != "image/jpeg" || got.Metadata["shot"] != "morning" {
				t.Fatalf("GetObject content type %q metadata %v", aws.ToString(got.ContentType), got.Metadata)
			}

			ranged, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("photos"), Key: aws.String("albums/2025/cat.jpg"), Range: aws.String("bytes=100-199")})
			if err != nil {
				t.Fatalf("ranged GetObject: %v", err)
			}
			slice, _ := io.ReadAll(ranged.Body)
			_ = ranged.Body.Close()
			if !bytes.Equal(slice, body[100:200]) {
				t.Fatalf("ranged body mismatch")
			}

			head, err := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String("photos"), Key: aws.String("albums/2025/cat.jpg")})
			if err != nil {
				t.Fatalf("HeadObject: %v", err)
			}
			if aws.ToInt64(head.ContentLength) != int64(len(body)) || aws.ToString(head.ETag) != wantETag {
				t.Fatalf("HeadObject length %d ETag %s", aws.ToInt64(head.ContentLength), aws.ToString(head.ETag))
			}

			assertListing(t, client)

			if _, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String("photos"), Key: aws.String("albums/2025/cat.jpg")}); err != nil {
				t.Fatalf("DeleteObject: %v", err)
			}
			_, err = client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("photos"), Key: aws.String("albums/2025/cat.jpg")})
			var missing *types.NoSuchKey
			if !errors.As(err, &missing) {
				t.Fatalf("GetObject after delete = %v, want NoSuchKey", err)
			}
		})
	}
}

func assertListing(t *testing.T, client *s3.Client) {
	t.Helper()
	ctx := context.Background()
	buckets, err := client.ListBuckets(ctx, &s3.ListBucketsInput{})
	if err != nil || len(buckets.Buckets) != 1 || aws.ToString(buckets.Buckets[0].Name) != "photos" {
		t.Fatalf("ListBuckets = %+v, %v", buckets, err)
	}
	flat, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String("photos"), Prefix: aws.String("albums/")})
	if err != nil {
		t.Fatalf("ListObjectsV2: %v", err)
	}
	if len(flat.Contents) != 2 || aws.ToString(flat.Contents[0].Key) != "albums/2025/cat.jpg" || aws.ToString(flat.Contents[1].Key) != "albums/readme.txt" {
		t.Fatalf("ListObjectsV2 contents = %+v", flat.Contents)
	}
	grouped, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String("photos"), Prefix: aws.String("albums/"), Delimiter: aws.String("/")})
	if err != nil {
		t.Fatalf("ListObjectsV2 delimiter: %v", err)
	}
	if len(grouped.Contents) != 1 || len(grouped.CommonPrefixes) != 1 || aws.ToString(grouped.CommonPrefixes[0].Prefix) != "albums/2025/" {
		t.Fatalf("ListObjectsV2 delimiter = %+v / %+v", grouped.Contents, grouped.CommonPrefixes)
	}
	paged, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String("photos"), MaxKeys: aws.Int32(1)})
	if err != nil || !aws.ToBool(paged.IsTruncated) || paged.NextContinuationToken == nil {
		t.Fatalf("paged ListObjectsV2 = %+v, %v", paged, err)
	}
	next, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String("photos"), ContinuationToken: paged.NextContinuationToken})
	if err != nil || len(next.Contents) != 1 {
		t.Fatalf("second page = %+v, %v", next, err)
	}
}
