package phase08

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestSigV4HeaderAndPresignedRequestsAuthenticate(t *testing.T) {
	w := newWorld(t)
	w.createBucket("photos", "none")
	client := w.sdk(w.keyID, w.secret)
	presigner := s3.NewPresignClient(client)
	ctx := context.Background()
	body := []byte("signed in many ways")
	if _, err := client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String("photos"), Key: aws.String("signed.txt"), Body: bytes.NewReader(body)}); err != nil {
		t.Fatalf("header-signed PutObject: %v", err)
	}
	if _, err := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String("photos"), Key: aws.String("signed.txt")}); err != nil {
		t.Fatalf("header-signed HeadObject: %v", err)
	}

	t.Run("presigned GET", func(t *testing.T) {
		signed, err := presigner.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("photos"), Key: aws.String("signed.txt")}, s3.WithPresignExpires(5*time.Minute))
		if err != nil {
			t.Fatalf("PresignGetObject: %v", err)
		}
		status, got := do(t, http.MethodGet, signed.URL, nil)
		if status != http.StatusOK || !bytes.Equal(got, body) {
			t.Fatalf("presigned GET = %d %q", status, got)
		}
	})

	t.Run("presigned PUT", func(t *testing.T) {
		signed, err := presigner.PresignPutObject(ctx, &s3.PutObjectInput{Bucket: aws.String("photos"), Key: aws.String("uploaded.txt")}, s3.WithPresignExpires(5*time.Minute))
		if err != nil {
			t.Fatalf("PresignPutObject: %v", err)
		}
		upload := []byte("uploaded through a presigned url")
		status, answer := do(t, http.MethodPut, signed.URL, upload)
		if status != http.StatusOK {
			t.Fatalf("presigned PUT = %d %s", status, answer)
		}
		fetched, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("photos"), Key: aws.String("uploaded.txt")})
		if err != nil {
			t.Fatalf("GetObject after presigned PUT: %v", err)
		}
		data, _ := io.ReadAll(fetched.Body)
		_ = fetched.Body.Close()
		if !bytes.Equal(data, upload) {
			t.Fatalf("object after presigned PUT = %q", data)
		}
	})

	t.Run("presigned request with a tampered signature", func(t *testing.T) {
		signed, err := presigner.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("photos"), Key: aws.String("signed.txt")})
		if err != nil {
			t.Fatalf("PresignGetObject: %v", err)
		}
		tampered := strings.Replace(signed.URL, "X-Amz-Signature=", "X-Amz-Signature=00", 1)
		status, answer := do(t, http.MethodGet, tampered, nil)
		if status != http.StatusForbidden || !strings.Contains(string(answer), "<Code>SignatureDoesNotMatch</Code>") {
			t.Fatalf("tampered presigned GET = %d %s", status, answer)
		}
	})

	t.Run("expired presigned request", func(t *testing.T) {
		signed, err := presigner.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("photos"), Key: aws.String("signed.txt")}, s3.WithPresignExpires(time.Minute))
		if err != nil {
			t.Fatalf("PresignGetObject: %v", err)
		}
		w.clk.Advance(2 * time.Minute)
		status, answer := do(t, http.MethodGet, signed.URL, nil)
		if status != http.StatusForbidden || !strings.Contains(string(answer), "<Code>AccessDenied</Code>") {
			t.Fatalf("expired presigned GET = %d %s", status, answer)
		}
	})

	t.Run("unsigned request to a private bucket", func(t *testing.T) {
		status, answer := do(t, http.MethodGet, w.server.URL+"/photos/signed.txt", nil)
		if status != http.StatusForbidden || !strings.Contains(string(answer), "<Code>AccessDenied</Code>") {
			t.Fatalf("anonymous GET = %d %s", status, answer)
		}
	})
}

func do(t *testing.T, method, target string, body []byte) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, target, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, target, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return resp.StatusCode, data
}
