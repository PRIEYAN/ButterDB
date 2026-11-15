package sigv4

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestCrossCheckWithSDKSigner(t *testing.T) {
	body := "cross check body"
	digest := sha256.Sum256([]byte(body))
	bodyHash := hex.EncodeToString(digest[:])
	tests := []struct {
		name string
		req  sdkRequest
		mode PayloadMode
	}{
		{name: "get object", req: sdkRequest{method: http.MethodGet, target: "/bucket/key.txt", payloadHash: emptyPayloadSHA256}, mode: PayloadSignedHex},
		{name: "get with sdk x-id parameter", req: sdkRequest{method: http.MethodGet, target: "/bucket/key.txt?x-id=GetObject", payloadHash: emptyPayloadSHA256}, mode: PayloadSignedHex},
		{name: "put unsigned payload", req: sdkRequest{method: http.MethodPut, target: "/bucket/key.txt", body: body}, mode: PayloadUnsigned},
		{name: "put signed payload", req: sdkRequest{method: http.MethodPut, target: "/bucket/key.txt", body: body, payloadHash: bodyHash}, mode: PayloadSignedHex},
		{name: "key with special characters", req: sdkRequest{method: http.MethodPut, target: uriEncode("/bucket/dir/a b+c(1)!*'$&=@;:,.txt", false), body: body}, mode: PayloadUnsigned},
		{name: "unicode key", req: sdkRequest{method: http.MethodGet, target: "/bucket/%E2%82%AC%20%F0%9F%A7%88.txt", payloadHash: emptyPayloadSHA256}, mode: PayloadSignedHex},
		{name: "list with query", req: sdkRequest{method: http.MethodGet, target: "/bucket?list-type=2&prefix=a%20b%2Fc&delimiter=%2F&max-keys=10", payloadHash: emptyPayloadSHA256}, mode: PayloadSignedHex},
		{name: "valueless subresource", req: sdkRequest{method: http.MethodGet, target: "/bucket?versioning", payloadHash: emptyPayloadSHA256}, mode: PayloadSignedHex},
		{name: "repeated query name", req: sdkRequest{method: http.MethodGet, target: "/bucket?tag=b&tag=a", payloadHash: emptyPayloadSHA256}, mode: PayloadSignedHex},
		{name: "upload part", req: sdkRequest{method: http.MethodPut, target: "/bucket/key?partNumber=3&uploadId=abc%2Bdef", body: body}, mode: PayloadUnsigned},
		{name: "signed metadata headers", req: sdkRequest{method: http.MethodPut, target: "/bucket/key", body: body, headers: map[string]string{"Content-Type": "text/plain", "X-Amz-Meta-Colour": "dark   blue", "Cache-Control": "no-cache"}}, mode: PayloadUnsigned},
		{name: "host with port", req: sdkRequest{method: http.MethodGet, target: "/bucket/key", host: "127.0.0.1:9000", payloadHash: emptyPayloadSHA256}, mode: PayloadSignedHex},
		{name: "date in another region", req: sdkRequest{method: http.MethodGet, target: "/bucket/key", payloadHash: emptyPayloadSHA256, region: "us-east-1"}, mode: PayloadSignedHex},
		{name: "streaming unsigned trailer token", req: sdkRequest{method: http.MethodPut, target: "/bucket/key", payloadHash: hashStreamingUnsignedTrailer, headers: map[string]string{"X-Amz-Decoded-Content-Length": "4", "X-Amz-Trailer": "x-amz-checksum-crc32"}}, mode: PayloadStreamingUnsignedTrailer},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := newTestEnv(t, "us-east-1")
			req := test.req.build(t)
			auth, err := env.verifier.Authenticate(t.Context(), req)
			if err != nil {
				t.Fatalf("Authenticate() error = %v", err)
			}
			if auth.Kind != AuthHeader || auth.Mode != test.mode {
				t.Fatalf("Auth = %+v", auth)
			}
			if test.mode == PayloadUnsigned || (test.mode == PayloadSignedHex && test.req.body != "") {
				reader, err := env.verifier.Body(req, auth)
				if err != nil {
					t.Fatal(err)
				}
				got, err := io.ReadAll(reader)
				if err != nil || string(got) != body {
					t.Fatalf("ReadAll() = %q, %v", got, err)
				}
			}
		})
	}
	t.Run("sdk presigned urls verify", func(t *testing.T) {
		env := newTestEnv(t, "us-east-1")
		for _, target := range []string{"/bucket/key.txt", uriEncode("/bucket/a b+c(1)!*'.txt", false), "/bucket/key?response-content-type=text%2Fplain&x-id=GetObject"} {
			req := sdkPresigned(t, http.MethodGet, target, time.Hour, baseTime)
			if _, err := env.verifier.Authenticate(t.Context(), req); err != nil {
				t.Fatalf("Authenticate(%q) error = %v", target, err)
			}
		}
	})
	t.Run("wrong secret fails", func(t *testing.T) {
		env := newTestEnv(t, "us-east-1")
		req := sdkRequest{method: http.MethodGet, target: "/bucket/key", payloadHash: emptyPayloadSHA256, secret: strings.Repeat("x", 40), keyID: testKeyID}.build(t)
		_, err := env.verifier.Authenticate(t.Context(), req)
		requireReason(t, err, ReasonSignature)
	})
}
