package multipart

import (
	"encoding/xml"
	"net/http"
	"strings"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/access/sigv4"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/ingest/multipartstage"
	"github.com/PRIEYAN/ButterDB/internal/s3api/router"
)

func createSpec(headers map[string]string) requestSpec {
	return requestSpec{method: http.MethodPost, target: "/photos/video/clip.mp4?uploads", headers: headers}
}

func TestCreateMultipartUpload(t *testing.T) {
	tdeBucket := map[bucket.BucketName]bucket.Info{
		testBucket: {Name: testBucket, ChannelID: testChannelID, Policy: bucket.PolicyPrivate, DefaultEncryption: bucket.EncryptionTDE1},
	}
	tests := []struct {
		name       string
		headers    map[string]string
		infos      map[bucket.BucketName]bucket.Info
		wantStatus int
		wantCode   string
		check      func(t *testing.T, req multipartstage.CreateRequest, header http.Header)
	}{
		{
			name:       "plain request uses defaults",
			wantStatus: http.StatusOK,
			check: func(t *testing.T, req multipartstage.CreateRequest, header http.Header) {
				if req.ContentType != "application/octet-stream" || req.Encrypted || req.BucketChannelID != testChannelID {
					t.Fatalf("request = %+v", req)
				}
				if req.AccessKeyID != testAccessKey || string(req.Key) != testKey || string(req.Bucket) != testBucket {
					t.Fatalf("request identity = %+v", req)
				}
				if header.Get(serverSideEncryption) != "" {
					t.Fatalf("unexpected encryption header %q", header.Get(serverSideEncryption))
				}
			},
		},
		{
			name: "metadata and system headers are stored",
			headers: map[string]string{
				"Content-Type":        "video/mp4",
				"Cache-Control":       "max-age=60",
				"Content-Encoding":    "aws-chunked,gzip",
				"X-Amz-Meta-Author":   "=?UTF-8?B?w6l2YQ==?=",
				"X-Amz-Meta-Project":  "butter",
				"X-Amz-Storage-Class": "STANDARD",
				"X-Amz-Acl":           "private",
			},
			wantStatus: http.StatusOK,
			check: func(t *testing.T, req multipartstage.CreateRequest, _ http.Header) {
				if req.ContentType != "video/mp4" {
					t.Fatalf("content type = %q", req.ContentType)
				}
				if req.UserMeta["author"] != "éva" || req.UserMeta["project"] != "butter" || len(req.UserMeta) != 2 {
					t.Fatalf("user meta = %v", req.UserMeta)
				}
				if req.SystemMeta["cache-control"] != "max-age=60" || req.SystemMeta["content-encoding"] != "gzip" || len(req.SystemMeta) != 2 {
					t.Fatalf("system meta = %v", req.SystemMeta)
				}
			},
		},
		{
			name:       "explicit AES256 encrypts",
			headers:    map[string]string{"X-Amz-Server-Side-Encryption": "AES256"},
			wantStatus: http.StatusOK,
			check: func(t *testing.T, req multipartstage.CreateRequest, header http.Header) {
				if !req.Encrypted || header.Get(serverSideEncryption) != "AES256" {
					t.Fatalf("encrypted = %v header = %q", req.Encrypted, header.Get(serverSideEncryption))
				}
			},
		},
		{
			name:       "bucket default encryption applies",
			infos:      tdeBucket,
			wantStatus: http.StatusOK,
			check: func(t *testing.T, req multipartstage.CreateRequest, header http.Header) {
				if !req.Encrypted || header.Get(serverSideEncryption) != "AES256" {
					t.Fatalf("encrypted = %v header = %q", req.Encrypted, header.Get(serverSideEncryption))
				}
			},
		},
		{
			name:       "checksum algorithm and type are echoed",
			headers:    map[string]string{"X-Amz-Checksum-Algorithm": "CRC32", "X-Amz-Checksum-Type": "COMPOSITE"},
			wantStatus: http.StatusOK,
			check: func(t *testing.T, req multipartstage.CreateRequest, header http.Header) {
				if req.ChecksumAlgorithm != "CRC32" || header.Get("X-Amz-Checksum-Algorithm") != "CRC32" || header.Get("X-Amz-Checksum-Type") != "COMPOSITE" {
					t.Fatalf("request = %+v header = %v", req, header)
				}
			},
		},
		{name: "kms encryption refused", headers: map[string]string{"X-Amz-Server-Side-Encryption": "aws:kms"}, wantStatus: 400, wantCode: "InvalidArgument"},
		{name: "customer key encryption refused", headers: map[string]string{"X-Amz-Server-Side-Encryption-Customer-Algorithm": "AES256"}, wantStatus: 501, wantCode: "NotImplemented"},
		{name: "kms key id refused", headers: map[string]string{"X-Amz-Server-Side-Encryption-Aws-Kms-Key-Id": "k"}, wantStatus: 501, wantCode: "NotImplemented"},
		{name: "object lock refused", headers: map[string]string{"X-Amz-Object-Lock-Mode": "GOVERNANCE"}, wantStatus: 501, wantCode: "NotImplemented"},
		{name: "tagging refused", headers: map[string]string{"X-Amz-Tagging": "a=b"}, wantStatus: 501, wantCode: "NotImplemented"},
		{name: "grant header refused", headers: map[string]string{"X-Amz-Grant-Read": "id=1"}, wantStatus: 400, wantCode: "AccessControlListNotSupported"},
		{name: "public read acl on private bucket refused", headers: map[string]string{"X-Amz-Acl": "public-read"}, wantStatus: 400, wantCode: "AccessControlListNotSupported"},
		{name: "storage class refused", headers: map[string]string{"X-Amz-Storage-Class": "GLACIER"}, wantStatus: 400, wantCode: "InvalidStorageClass"},
		{name: "unknown checksum algorithm", headers: map[string]string{"X-Amz-Checksum-Algorithm": "MD5"}, wantStatus: 400, wantCode: "InvalidArgument"},
		{name: "unknown checksum type", headers: map[string]string{"X-Amz-Checksum-Type": "OTHER"}, wantStatus: 400, wantCode: "InvalidArgument"},
		{name: "user metadata over 2048 bytes", headers: map[string]string{"X-Amz-Meta-Big": strings.Repeat("a", 2048)}, wantStatus: 400, wantCode: "MetadataTooLarge"},
		{name: "system header over 1024 bytes", headers: map[string]string{"Cache-Control": strings.Repeat("a", 1025)}, wantStatus: 400, wantCode: "InvalidArgument"},
		{name: "content type over 1024 bytes", headers: map[string]string{"Content-Type": strings.Repeat("a", 1025)}, wantStatus: 400, wantCode: "InvalidArgument"},
		{name: "put record over 4096 characters", headers: map[string]string{"X-Amz-Meta-K": strings.Repeat("é", 1000)}, wantStatus: 400, wantCode: "MetadataTooLarge"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fx := newFixture(t, fixtureOptions{infos: test.infos})
			rec := serve(fx.handlers.CreateMultipartUpload, createSpec(test.headers), routeFor("CreateMultipartUpload"), writerAuth())
			if test.wantCode != "" {
				assertErrorCode(t, rec, test.wantStatus, test.wantCode)
				if len(fx.stage.createRequest) != 0 {
					t.Fatalf("stage.Create called for a refused request")
				}
				return
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
			}
			var result initiateResult
			if err := xml.Unmarshal(rec.Body.Bytes()[strings.Index(rec.Body.String(), "<Initiate"):], &result); err != nil {
				t.Fatalf("decode result: %v", err)
			}
			if result.Bucket != testBucket || result.Key != testKey || result.UploadID != testUploadID {
				t.Fatalf("result = %+v", result)
			}
			if !strings.Contains(rec.Body.String(), `xmlns="http://s3.amazonaws.com/doc/2006-03-01/"`) {
				t.Fatalf("namespace missing: %s", rec.Body.String())
			}
			test.check(t, fx.stage.createRequest[0], rec.Header())
		})
	}
}

func TestCreateMultipartUploadRejections(t *testing.T) {
	tooLong := routeFor("CreateMultipartUpload")
	tooLong.Target.Key = strings.Repeat("a", 1025)
	controlKey := routeFor("CreateMultipartUpload")
	controlKey.Target.Key = "bad\x01key"
	tests := []struct {
		name       string
		route      func() (rt routeAndAuth)
		stageErr   error
		wantStatus int
		wantCode   string
	}{
		{name: "key too long", route: func() routeAndAuth { return routeAndAuth{tooLong, writerAuth()} }, wantStatus: 400, wantCode: "KeyTooLongError"},
		{name: "control character key", route: func() routeAndAuth { return routeAndAuth{controlKey, writerAuth()} }, wantStatus: 400, wantCode: "InvalidArgument"},
		{name: "anonymous caller", route: func() routeAndAuth { return routeAndAuth{routeFor("CreateMultipartUpload"), sigv4.Auth{}} }, wantStatus: 403, wantCode: "AccessDenied"},
		{name: "read only key", route: func() routeAndAuth {
			return routeAndAuth{routeFor("CreateMultipartUpload"), authFor([]string{"*"}, []string{"read"})}
		}, wantStatus: 403, wantCode: "AccessDenied"},
		{name: "bucket outside scope", route: func() routeAndAuth {
			return routeAndAuth{routeFor("CreateMultipartUpload"), authFor([]string{"archive"}, []string{"write"})}
		}, wantStatus: 403, wantCode: "AccessDenied"},
		{name: "stage failure", route: func() routeAndAuth { return routeAndAuth{routeFor("CreateMultipartUpload"), writerAuth()} }, stageErr: errs.ErrMissingDependency, wantStatus: 500, wantCode: "InternalError"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fx := newFixture(t, fixtureOptions{})
			fx.stage.createErr = test.stageErr
			chosen := test.route()
			rec := serve(fx.handlers.CreateMultipartUpload, createSpec(nil), chosen.route, chosen.auth)
			assertErrorCode(t, rec, test.wantStatus, test.wantCode)
		})
	}
}

type routeAndAuth struct {
	route router.Route
	auth  sigv4.Auth
}
