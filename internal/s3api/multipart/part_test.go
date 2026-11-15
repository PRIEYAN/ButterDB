package multipart

import (
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/access/sigv4"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/limits"
	"github.com/PRIEYAN/ButterDB/internal/ingest/multipartstage"
	"github.com/PRIEYAN/ButterDB/internal/s3api/router"
)

func partTarget(number string) string {
	return "/photos/video/clip.mp4?partNumber=" + number + "&uploadId=" + testUploadID
}

func uploadPart(fx *fixture, spec requestSpec, a sigv4.Auth) *httptest.ResponseRecorder {
	return serve(fx.handlers.UploadPart, spec, routeFor("UploadPart"), a)
}

func declaredLengthRequest(target string, body io.Reader, length int64, headers map[string]string) *http.Request {
	r := httptest.NewRequest(http.MethodPut, target, body)
	r.ContentLength = length
	for name, value := range headers {
		r.Header.Set(name, value)
	}
	return r
}

func TestPartNumberRange(t *testing.T) {
	tests := []struct {
		name       string
		number     string
		wantStatus int
	}{
		{name: "lowest valid", number: "1", wantStatus: http.StatusOK},
		{name: "highest valid", number: "10000", wantStatus: http.StatusOK},
		{name: "zero", number: "0", wantStatus: http.StatusBadRequest},
		{name: "one past the highest", number: "10001", wantStatus: http.StatusBadRequest},
		{name: "negative", number: "-1", wantStatus: http.StatusBadRequest},
		{name: "explicit plus sign", number: "%2B5", wantStatus: http.StatusBadRequest},
		{name: "not a number", number: "abc", wantStatus: http.StatusBadRequest},
		{name: "empty", number: "", wantStatus: http.StatusBadRequest},
		{name: "overflowing", number: "99999999999999999999", wantStatus: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fx := newFixture(t, fixtureOptions{})
			rec := uploadPart(fx, requestSpec{method: http.MethodPut, target: partTarget(test.number), body: "hello"}, writerAuth())
			if rec.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d, body %s", rec.Code, test.wantStatus, rec.Body.String())
			}
			if test.wantStatus == http.StatusOK {
				return
			}
			parsed := assertErrorCode(t, rec, http.StatusBadRequest, "InvalidArgument")
			if parsed.Message != "Part number must be an integer between 1 and 10000, inclusive" {
				t.Fatalf("message = %q", parsed.Message)
			}
			if len(fx.stage.putCalls) != 0 {
				t.Fatalf("stage.PutPart called %d times for a rejected part number", len(fx.stage.putCalls))
			}
		})
	}
}

func TestUploadPartStagesBody(t *testing.T) {
	fx := newFixture(t, fixtureOptions{})
	rec := uploadPart(fx, requestSpec{method: http.MethodPut, target: partTarget("3"), body: "hello world"}, writerAuth())
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("status = %d body %q", rec.Code, rec.Body.String())
	}
	want := `"` + md5Hex([]byte("hello world")) + `"`
	if got := rec.Header().Get("ETag"); got != want {
		t.Fatalf("ETag = %q, want %q", got, want)
	}
	call := fx.stage.putCalls[0]
	if call.uploadID != testUploadID || call.number != 3 || call.size != 11 || string(call.body) != "hello world" {
		t.Fatalf("put call = %+v", call)
	}
	if rec.Header().Get(serverSideEncryption) != "" {
		t.Fatalf("plaintext upload carries an encryption header")
	}
}

func TestUploadPartEncryptedUploadHeader(t *testing.T) {
	record := openRecord()
	record.Encrypted = true
	fx := newFixture(t, fixtureOptions{records: map[string]UploadRecord{testUploadID: record}})
	rec := uploadPart(fx, requestSpec{method: http.MethodPut, target: partTarget("1"), body: "x"}, writerAuth())
	if rec.Code != http.StatusOK || rec.Header().Get(serverSideEncryption) != "AES256" {
		t.Fatalf("status = %d header = %q", rec.Code, rec.Header().Get(serverSideEncryption))
	}
}

func TestUploadPartUploadStates(t *testing.T) {
	other := openRecord()
	other.Key = "other/key"
	otherBucket := openRecord()
	otherBucket.Bucket = "archive"
	committing := openRecord()
	committing.State = stateCommitting
	completed := openRecord()
	completed.State = stateCompleted
	aborted := openRecord()
	aborted.State = stateAborted
	tests := []struct {
		name       string
		record     *UploadRecord
		target     string
		wantStatus int
		wantCode   string
	}{
		{name: "open upload", record: ptr(openRecord()), wantStatus: 200},
		{name: "committing upload", record: &committing, wantStatus: 409, wantCode: "OperationAborted"},
		{name: "completed upload", record: &completed, wantStatus: 404, wantCode: "NoSuchUpload"},
		{name: "aborted upload", record: &aborted, wantStatus: 404, wantCode: "NoSuchUpload"},
		{name: "upload of another key", record: &other, wantStatus: 404, wantCode: "NoSuchUpload"},
		{name: "upload of another bucket", record: &otherBucket, wantStatus: 404, wantCode: "NoSuchUpload"},
		{name: "unknown upload", target: "/photos/video/clip.mp4?partNumber=1&uploadId=00000000000000000000000000000000", wantStatus: 404, wantCode: "NoSuchUpload"},
		{name: "missing upload id", target: "/photos/video/clip.mp4?partNumber=1", wantStatus: 404, wantCode: "NoSuchUpload"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			records := map[string]UploadRecord{}
			if test.record != nil {
				records[testUploadID] = *test.record
			}
			fx := newFixture(t, fixtureOptions{records: records})
			target := test.target
			if target == "" {
				target = partTarget("1")
			}
			rec := uploadPart(fx, requestSpec{method: http.MethodPut, target: target, body: "data"}, writerAuth())
			if test.wantCode == "" {
				if rec.Code != test.wantStatus {
					t.Fatalf("status = %d", rec.Code)
				}
				return
			}
			assertErrorCode(t, rec, test.wantStatus, test.wantCode)
			if len(fx.stage.putCalls) != 0 {
				t.Fatalf("body was handed to the stage for a refused upload")
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

func TestRunningTotalLimit(t *testing.T) {
	maxStored := int64(limits.MaxStoredObjectBytes)
	maxPlain := int64(limits.MaxEncryptedPlaintextBytes)
	tests := []struct {
		name      string
		encrypted bool
		staged    []multipartstage.Part
		number    string
		length    int64
		wantCode  string
	}{
		{name: "single part over the stored ceiling", number: "1", length: maxStored + 1, wantCode: "EntityTooLarge"},
		{name: "single part at the stored ceiling", number: "1", length: maxStored},
		{name: "second part pushes the total over", staged: []multipartstage.Part{{Number: 1, Size: maxStored - 50}}, number: "2", length: 100, wantCode: "EntityTooLarge"},
		{name: "second part fits exactly", staged: []multipartstage.Part{{Number: 1, Size: maxStored - 100}}, number: "2", length: 100},
		{name: "replacing a part counts the latest size once", staged: []multipartstage.Part{{Number: 1, Size: maxStored}}, number: "1", length: 100},
		{name: "encrypted part over the plaintext ceiling", encrypted: true, number: "1", length: maxPlain + 1, wantCode: "EntityTooLarge"},
		{name: "encrypted total over the plaintext ceiling", encrypted: true, staged: []multipartstage.Part{{Number: 1, Size: maxPlain - 10}}, number: "2", length: 100, wantCode: "EntityTooLarge"},
		{name: "encrypted total at the plaintext ceiling", encrypted: true, staged: []multipartstage.Part{{Number: 1, Size: maxPlain - 100}}, number: "2", length: 100},
		{name: "same total is fine when unencrypted", staged: []multipartstage.Part{{Number: 1, Size: maxPlain - 10}}, number: "2", length: 100},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := openRecord()
			record.Encrypted = test.encrypted
			fx := newFixture(t, fixtureOptions{records: map[string]UploadRecord{testUploadID: record}})
			fx.stage.setParts(test.staged...)
			body := &countingReader{src: strings.NewReader(strings.Repeat("a", 100))}
			rec := serveRequestWithRoute(fx.handlers.UploadPart, declaredLengthRequest(partTarget(test.number), body, test.length, nil))
			if test.wantCode == "" {
				if len(fx.stage.putCalls) != 1 {
					t.Fatalf("status = %d, put calls = %d, body %s", rec.Code, len(fx.stage.putCalls), rec.Body.String())
				}
				return
			}
			parsed := assertErrorCode(t, rec, http.StatusBadRequest, test.wantCode)
			if parsed.field("ProposedSize") == "" || parsed.field("MaxSizeAllowed") != strconv.FormatInt(maxStored, 10) {
				t.Fatalf("size fields missing: %+v", parsed)
			}
			if body.read != 0 || len(fx.stage.putCalls) != 0 {
				t.Fatalf("body was read (%d bytes) or staged before the limit check", body.read)
			}
		})
	}
}

func serveRequestWithRoute(h func(http.ResponseWriter, *http.Request, router.Route, sigv4.Auth), r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	rec.Header().Set("X-Amz-Request-Id", "4F2A9C1D7E3B5A60")
	rec.Header().Set("X-Amz-Id-2", "9Kp3m1sQ0n2bW8vJcX4yZ6tR5uA7eD0fG1hI2jK3lM4=")
	h(rec, r, routeFor("UploadPart"), writerAuth())
	return rec
}

func TestUploadPartLengthFraming(t *testing.T) {
	streaming := writerAuth()
	streaming.Mode = sigv4.PayloadStreamingUnsignedTrailer
	tests := []struct {
		name       string
		length     int64
		auth       sigv4.Auth
		wantStatus int
		wantCode   string
	}{
		{name: "missing content length", length: -1, auth: writerAuth(), wantStatus: 411, wantCode: "MissingContentLength"},
		{name: "streaming without decoded length", length: -1, auth: streaming, wantStatus: 411, wantCode: "MissingContentLength"},
		{name: "empty part", length: 0, auth: writerAuth(), wantStatus: 200},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fx := newFixture(t, fixtureOptions{})
			r := declaredLengthRequest(partTarget("1"), strings.NewReader(""), test.length, nil)
			rec := httptest.NewRecorder()
			fx.handlers.UploadPart(rec, r, routeFor("UploadPart"), test.auth)
			if test.wantCode != "" {
				assertErrorCode(t, rec, test.wantStatus, test.wantCode)
				return
			}
			if rec.Code != test.wantStatus {
				t.Fatalf("status = %d", rec.Code)
			}
		})
	}
}

func crc32Base64(data []byte) string {
	var raw [4]byte
	binary.BigEndian.PutUint32(raw[:], crc32.ChecksumIEEE(data))
	return base64.StdEncoding.EncodeToString(raw[:])
}

func TestPartChecksumVerified(t *testing.T) {
	payload := []byte("part checksum payload")
	md5Header := base64.StdEncoding.EncodeToString(mustDecodeHex(md5Hex(payload)))
	wrongMD5 := base64.StdEncoding.EncodeToString(mustDecodeHex(md5Hex([]byte("other"))))
	tests := []struct {
		name       string
		headers    map[string]string
		wantStatus int
		wantCode   string
		wantStaged bool
		wantEcho   string
	}{
		{name: "matching crc32 is echoed", headers: map[string]string{"X-Amz-Checksum-Crc32": crc32Base64(payload)}, wantStatus: 200, wantStaged: true, wantEcho: crc32Base64(payload)},
		{name: "mismatching crc32", headers: map[string]string{"X-Amz-Checksum-Crc32": crc32Base64([]byte("other"))}, wantStatus: 400, wantCode: "BadDigest"},
		{name: "malformed crc32", headers: map[string]string{"X-Amz-Checksum-Crc32": "not-base64!"}, wantStatus: 400, wantCode: "InvalidRequest"},
		{name: "two checksum headers", headers: map[string]string{"X-Amz-Checksum-Crc32": crc32Base64(payload), "X-Amz-Checksum-Sha1": "AAAAAAAAAAAAAAAAAAAAAAAAAAA="}, wantStatus: 400, wantCode: "InvalidRequest"},
		{name: "matching content md5", headers: map[string]string{"Content-MD5": md5Header}, wantStatus: 200, wantStaged: true},
		{name: "mismatching content md5", headers: map[string]string{"Content-MD5": wrongMD5}, wantStatus: 400, wantCode: "BadDigest"},
		{name: "malformed content md5", headers: map[string]string{"Content-MD5": "%%%"}, wantStatus: 400, wantCode: "InvalidDigest"},
		{name: "short content md5", headers: map[string]string{"Content-MD5": base64.StdEncoding.EncodeToString([]byte("short"))}, wantStatus: 400, wantCode: "InvalidDigest"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fx := newFixture(t, fixtureOptions{})
			rec := uploadPart(fx, requestSpec{method: http.MethodPut, target: partTarget("1"), body: string(payload), headers: test.headers}, writerAuth())
			if test.wantCode != "" {
				assertErrorCode(t, rec, test.wantStatus, test.wantCode)
			} else if rec.Code != test.wantStatus {
				t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
			}
			if _, staged := fx.stage.parts[1]; staged != test.wantStaged {
				t.Fatalf("part staged = %v, want %v", staged, test.wantStaged)
			}
			if test.wantEcho != "" && rec.Header().Get("X-Amz-Checksum-Crc32") != test.wantEcho {
				t.Fatalf("echoed checksum = %q, want %q", rec.Header().Get("X-Amz-Checksum-Crc32"), test.wantEcho)
			}
		})
	}
}

func TestUploadPartStageFailures(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "upload became busy", err: errs.ErrUploadBusy, wantStatus: 409, wantCode: "OperationAborted"},
		{name: "upload vanished", err: errs.ErrNoSuchUpload, wantStatus: 404, wantCode: "NoSuchUpload"},
		{name: "slow down", err: errs.ErrSlowDown, wantStatus: 503, wantCode: "SlowDown"},
		{name: "short body", err: io.ErrUnexpectedEOF, wantStatus: 400, wantCode: "IncompleteBody"},
		{name: "body idle timeout", err: os.ErrDeadlineExceeded, wantStatus: 400, wantCode: "RequestTimeout"},
		{name: "unmapped failure", err: io.ErrClosedPipe, wantStatus: 500, wantCode: "InternalError"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fx := newFixture(t, fixtureOptions{})
			fx.stage.putErr = test.err
			rec := uploadPart(fx, requestSpec{method: http.MethodPut, target: partTarget("1"), body: "data"}, writerAuth())
			assertErrorCode(t, rec, test.wantStatus, test.wantCode)
		})
	}
}

func TestUploadPartWithBodyIdleTimeout(t *testing.T) {
	fx := newFixture(t, fixtureOptions{config: Config{BodyIdleTimeout: time.Minute}})
	rec := uploadPart(fx, requestSpec{method: http.MethodPut, target: partTarget("1"), body: "payload"}, writerAuth())
	if rec.Code != http.StatusOK || string(fx.stage.putCalls[0].body) != "payload" {
		t.Fatalf("status = %d calls = %+v", rec.Code, fx.stage.putCalls)
	}
}
