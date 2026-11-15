package phase08

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/PRIEYAN/ButterDB/internal/domain/access"
)

func apiErrorCode(err error) string {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode()
	}
	return ""
}

func httpStatus(err error) int {
	var responseErr *smithyhttp.ResponseError
	if errors.As(err, &responseErr) {
		return responseErr.HTTPStatusCode()
	}
	return 0
}

func TestXMLErrorsCarryStableS3Codes(t *testing.T) {
	w := newWorld(t)
	w.createBucket("photos", "none")
	w.createBucket("private-notes", "none")
	scope, err := access.NewScope([]string{"photos"}, []string{"read", "list"})
	if err != nil {
		t.Fatal(err)
	}
	readOnly, readOnlySecret, err := w.s3.Keys.Create(context.Background(), "photos reader", scope)
	if err != nil {
		t.Fatalf("create scoped key: %v", err)
	}
	valid := w.sdk(w.keyID, w.secret)
	if _, err := valid.PutObject(context.Background(), &s3.PutObjectInput{Bucket: aws.String("photos"), Key: aws.String("exists.txt"), Body: bytes.NewReader([]byte("x"))}); err != nil {
		t.Fatalf("seed object: %v", err)
	}

	tests := []struct {
		name       string
		client     *s3.Client
		call       func(client *s3.Client) error
		wantCode   string
		wantStatus int
	}{
		{
			name: "missing key", client: valid, wantCode: "NoSuchKey", wantStatus: http.StatusNotFound,
			call: func(c *s3.Client) error {
				_, err := c.GetObject(context.Background(), &s3.GetObjectInput{Bucket: aws.String("photos"), Key: aws.String("absent.txt")})
				return err
			},
		},
		{
			name: "missing bucket", client: valid, wantCode: "NoSuchBucket", wantStatus: http.StatusNotFound,
			call: func(c *s3.Client) error {
				_, err := c.ListObjectsV2(context.Background(), &s3.ListObjectsV2Input{Bucket: aws.String("no-such-bucket")})
				return err
			},
		},
		{
			name: "wrong secret", client: w.sdk(w.keyID, "not-the-secret-not-the-secret-not-the-sec"), wantCode: "SignatureDoesNotMatch", wantStatus: http.StatusForbidden,
			call: func(c *s3.Client) error {
				_, err := c.ListBuckets(context.Background(), &s3.ListBucketsInput{})
				return err
			},
		},
		{
			name: "unknown access key", client: w.sdk("BDBKUNKNOWNKEY000000", "irrelevant-secret"), wantCode: "InvalidAccessKeyId", wantStatus: http.StatusForbidden,
			call: func(c *s3.Client) error {
				_, err := c.ListBuckets(context.Background(), &s3.ListBucketsInput{})
				return err
			},
		},
		{
			name: "bucket outside the key scope", client: w.sdk(readOnly.ID.String(), readOnlySecret), wantCode: "AccessDenied", wantStatus: http.StatusForbidden,
			call: func(c *s3.Client) error {
				_, err := c.ListObjectsV2(context.Background(), &s3.ListObjectsV2Input{Bucket: aws.String("private-notes")})
				return err
			},
		},
		{
			name: "action outside the key scope", client: w.sdk(readOnly.ID.String(), readOnlySecret), wantCode: "AccessDenied", wantStatus: http.StatusForbidden,
			call: func(c *s3.Client) error {
				_, err := c.PutObject(context.Background(), &s3.PutObjectInput{Bucket: aws.String("photos"), Key: aws.String("denied.txt"), Body: bytes.NewReader([]byte("x"))})
				return err
			},
		},
		{
			name: "bucket creation is admin only", client: valid, wantCode: "AccessDenied", wantStatus: http.StatusForbidden,
			call: func(c *s3.Client) error {
				_, err := c.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String("brand-new-bucket")})
				return err
			},
		},
		{
			name: "invalid range", client: valid, wantCode: "InvalidRange", wantStatus: http.StatusRequestedRangeNotSatisfiable,
			call: func(c *s3.Client) error {
				_, err := c.GetObject(context.Background(), &s3.GetObjectInput{Bucket: aws.String("photos"), Key: aws.String("exists.txt"), Range: aws.String("bytes=500-600")})
				return err
			},
		},
		{
			name: "unknown multipart upload", client: valid, wantCode: "NoSuchUpload", wantStatus: http.StatusNotFound,
			call: func(c *s3.Client) error {
				_, err := c.AbortMultipartUpload(context.Background(), &s3.AbortMultipartUploadInput{
					Bucket: aws.String("photos"), Key: aws.String("exists.txt"), UploadId: aws.String("0123456789abcdef0123456789abcdef"),
				})
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call(tt.client)
			if err == nil {
				t.Fatalf("call succeeded, want %s", tt.wantCode)
			}
			if code := apiErrorCode(err); code != tt.wantCode {
				t.Fatalf("error code = %q, want %q (%v)", code, tt.wantCode, err)
			}
			if status := httpStatus(err); status != tt.wantStatus {
				t.Fatalf("HTTP status = %d, want %d", status, tt.wantStatus)
			}
		})
	}

	t.Run("error bodies are S3 XML with request ids and nosniff", func(t *testing.T) {
		resp, err := http.Get(w.server.URL + "/photos/exists.txt")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var body bytes.Buffer
		_, _ = body.ReadFrom(resp.Body)
		text := body.String()
		for _, want := range []string{"<Error>", "<Code>AccessDenied</Code>", "<RequestId>", "<HostId>"} {
			if !strings.Contains(text, want) {
				t.Fatalf("error body %q lacks %s", text, want)
			}
		}
		if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/xml") {
			t.Fatalf("Content-Type = %q", got)
		}
		if resp.Header.Get("X-Content-Type-Options") != "nosniff" || resp.Header.Get("X-Amz-Request-Id") == "" {
			t.Fatalf("headers = %v", resp.Header)
		}
	})
}
