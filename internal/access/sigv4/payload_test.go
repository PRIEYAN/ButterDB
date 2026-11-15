package sigv4

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"hash/crc64"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
)

const (
	hashStreamingSigned          = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD"
	hashStreamingSignedTrailer   = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER"
	hashStreamingUnsignedTrailer = "STREAMING-UNSIGNED-PAYLOAD-TRAILER"
)

func paramsFromAuth(a Auth) chunkParams {
	return chunkParams{
		signedChunks:  a.Mode != PayloadStreamingUnsignedTrailer,
		signedTrailer: a.Mode == PayloadStreamingSignedTrailer,
		trailerName:   a.Trailer,
		decodedLength: a.DecodedLength,
		signingKey:    a.signingKey,
		seed:          a.seed,
		requestTime:   a.RequestTime,
		region:        a.Region,
	}
}

func testChunkSignature(p chunkParams, previous string, data []byte) string {
	requestTime := p.requestTime.UTC()
	digest := sha256.Sum256(data)
	stringToSign := "AWS4-HMAC-SHA256-PAYLOAD\n" + requestTime.Format(timeLayout) + "\n" +
		credentialScopeText(requestTime.Format(dateLayout), p.region) + "\n" + previous + "\n" +
		emptyPayloadSHA256 + "\n" + hex.EncodeToString(digest[:])
	return hexSignature(p.signingKey, stringToSign)
}

func testTrailerSignature(p chunkParams, previous, name, value string) string {
	requestTime := p.requestTime.UTC()
	stringToSign := "AWS4-HMAC-SHA256-TRAILER\n" + requestTime.Format(timeLayout) + "\n" +
		credentialScopeText(requestTime.Format(dateLayout), p.region) + "\n" + previous + "\n" +
		hexSHA256(name+":"+value+"\n")
	return hexSignature(p.signingKey, stringToSign)
}

func encodeStream(p chunkParams, chunks [][]byte, trailerValue string) []byte {
	var out bytes.Buffer
	previous := p.seed
	for _, chunk := range chunks {
		if p.signedChunks {
			signature := testChunkSignature(p, previous, chunk)
			fmt.Fprintf(&out, "%x;chunk-signature=%s\r\n", len(chunk), signature)
			previous = signature
		} else {
			fmt.Fprintf(&out, "%x\r\n", len(chunk))
		}
		out.Write(chunk)
		out.WriteString("\r\n")
	}
	if p.signedChunks {
		signature := testChunkSignature(p, previous, nil)
		fmt.Fprintf(&out, "0;chunk-signature=%s\r\n", signature)
		previous = signature
	} else {
		out.WriteString("0\r\n")
	}
	if p.trailerName != "" {
		fmt.Fprintf(&out, "%s:%s\r\n", p.trailerName, trailerValue)
		if p.signedTrailer {
			fmt.Fprintf(&out, "x-amz-trailer-signature:%s\r\n", testTrailerSignature(p, previous, p.trailerName, trailerValue))
		}
	}
	out.WriteString("\r\n")
	return out.Bytes()
}

func crc32Base64(data []byte) string {
	sum := crc32.ChecksumIEEE(data)
	return base64.StdEncoding.EncodeToString([]byte{byte(sum >> 24), byte(sum >> 16), byte(sum >> 8), byte(sum)})
}

func splitChunks(data []byte, size int) [][]byte {
	var chunks [][]byte
	for len(data) > size {
		chunks = append(chunks, data[:size])
		data = data[size:]
	}
	if len(data) > 0 {
		chunks = append(chunks, data)
	}
	return chunks
}

func patternBytes(n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte('a' + i%26)
	}
	return out
}

type streamingFixture struct {
	env  *testEnv
	req  *http.Request
	auth Auth
}

func newStreamingFixture(t *testing.T, payloadHash string, decodedLength int, trailer string) streamingFixture {
	t.Helper()
	env := newTestEnv(t, "us-east-1")
	headers := map[string]string{"Content-Encoding": "aws-chunked"}
	if decodedLength >= 0 {
		headers["X-Amz-Decoded-Content-Length"] = strconv.Itoa(decodedLength)
	}
	if trailer != "" {
		headers["X-Amz-Trailer"] = trailer
	}
	req := sdkRequest{method: http.MethodPut, target: "/bucket/key", payloadHash: payloadHash, headers: headers}.build(t)
	auth, err := env.verifier.Authenticate(t.Context(), req)
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	return streamingFixture{env: env, req: req, auth: auth}
}

func (f streamingFixture) read(t *testing.T, encoded []byte) ([]byte, *PayloadReader, error) {
	t.Helper()
	f.req.Body = io.NopCloser(bytes.NewReader(encoded))
	reader, err := f.env.verifier.Body(f.req, f.auth)
	if err != nil {
		t.Fatalf("Body() error = %v", err)
	}
	out, err := io.ReadAll(reader)
	return out, reader, err
}

func TestUnsignedPayload(t *testing.T) {
	data := []byte("hello unsigned world")
	t.Run("plain body", func(t *testing.T) {
		env := newTestEnv(t, "us-east-1")
		req := sdkRequest{method: http.MethodPut, target: "/b/k", body: string(data)}.build(t)
		auth, err := env.verifier.Authenticate(t.Context(), req)
		if err != nil || auth.Mode != PayloadUnsigned {
			t.Fatalf("Authenticate() = %+v, %v", auth, err)
		}
		reader, err := env.verifier.Body(req, auth)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(reader)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("ReadAll() = %q, %v", got, err)
		}
		if algorithm, value := reader.Checksum(); algorithm != "" || value != "" {
			t.Fatalf("Checksum() = %q, %q", algorithm, value)
		}
		if err := reader.Close(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("matching crc32 header", func(t *testing.T) {
		env := newTestEnv(t, "us-east-1")
		req := sdkRequest{method: http.MethodPut, target: "/b/k", body: string(data), headers: map[string]string{"X-Amz-Checksum-Crc32": crc32Base64(data)}}.build(t)
		auth, _ := env.verifier.Authenticate(t.Context(), req)
		reader, err := env.verifier.Body(req, auth)
		if err != nil {
			t.Fatal(err)
		}
		if algorithm, _ := reader.Checksum(); algorithm != "" {
			t.Fatal("Checksum() reported before EOF")
		}
		if _, err := io.ReadAll(reader); err != nil {
			t.Fatal(err)
		}
		if algorithm, value := reader.Checksum(); algorithm != "crc32" || value != crc32Base64(data) {
			t.Fatalf("Checksum() = %q, %q", algorithm, value)
		}
	})
	t.Run("mismatching crc32 header", func(t *testing.T) {
		env := newTestEnv(t, "us-east-1")
		req := sdkRequest{method: http.MethodPut, target: "/b/k", body: string(data), headers: map[string]string{"X-Amz-Checksum-Crc32": crc32Base64([]byte("other"))}}.build(t)
		auth, _ := env.verifier.Authenticate(t.Context(), req)
		reader, err := env.verifier.Body(req, auth)
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.ReadAll(reader)
		typed := requireReason(t, err, ReasonChecksumMismatch)
		if typed.Algorithm != "crc32" || !errors.Is(err, errs.ErrBadDigest) {
			t.Fatalf("Error = %+v", typed)
		}
	})
}

func TestChecksumHeaderValidation(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
	}{
		{name: "bad base64", headers: map[string]string{"X-Amz-Checksum-Crc32": "!!!"}},
		{name: "wrong length", headers: map[string]string{"X-Amz-Checksum-Sha256": crc32Base64([]byte("x"))}},
		{name: "two algorithms", headers: map[string]string{"X-Amz-Checksum-Crc32": crc32Base64(nil), "X-Amz-Checksum-Sha1": base64.StdEncoding.EncodeToString(make([]byte, 20))}},
		{name: "empty value", headers: map[string]string{"X-Amz-Checksum-Crc32": ""}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := newTestEnv(t, "us-east-1")
			req := sdkRequest{method: http.MethodPut, target: "/b/k", body: "x", headers: test.headers}.build(t)
			if test.name == "empty value" {
				req.Header["X-Amz-Checksum-Crc32"] = []string{""}
			}
			auth, err := env.verifier.Authenticate(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			_, err = env.verifier.Body(req, auth)
			requireReason(t, err, ReasonChecksumMalformed)
		})
	}
	t.Run("ignored informational headers", func(t *testing.T) {
		env := newTestEnv(t, "us-east-1")
		req := sdkRequest{method: http.MethodPut, target: "/b/k", body: "x", headers: map[string]string{"X-Amz-Sdk-Checksum-Algorithm": "CRC32", "X-Amz-Checksum-Algorithm": "CRC32"}}.build(t)
		auth, _ := env.verifier.Authenticate(t.Context(), req)
		reader, err := env.verifier.Body(req, auth)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadAll(reader); err != nil {
			t.Fatal(err)
		}
	})
}

func TestSignedHexMismatch(t *testing.T) {
	data := []byte("signed hex payload")
	digest := sha256.Sum256(data)
	good := hex.EncodeToString(digest[:])
	t.Run("matching digest", func(t *testing.T) {
		env := newTestEnv(t, "us-east-1")
		req := sdkRequest{method: http.MethodPut, target: "/b/k", body: string(data), payloadHash: good}.build(t)
		auth, err := env.verifier.Authenticate(t.Context(), req)
		if err != nil || auth.Mode != PayloadSignedHex {
			t.Fatalf("Authenticate() = %+v, %v", auth, err)
		}
		reader, err := env.verifier.Body(req, auth)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(reader)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("ReadAll() = %q, %v", got, err)
		}
	})
	t.Run("body differs from digest", func(t *testing.T) {
		env := newTestEnv(t, "us-east-1")
		req := sdkRequest{method: http.MethodPut, target: "/b/k", body: string(data), payloadHash: good}.build(t)
		auth, _ := env.verifier.Authenticate(t.Context(), req)
		req.Body = io.NopCloser(strings.NewReader("tampered payload!!"))
		reader, err := env.verifier.Body(req, auth)
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.ReadAll(reader)
		typed := requireReason(t, err, ReasonContentSHA256Mismatch)
		wantServer := sha256.Sum256([]byte("tampered payload!!"))
		if typed.ClientSHA256 != good || typed.ServerSHA256 != hex.EncodeToString(wantServer[:]) {
			t.Fatalf("Error digests = %q / %q", typed.ClientSHA256, typed.ServerSHA256)
		}
		if !errors.Is(err, errs.ErrBadDigest) {
			t.Fatalf("error does not wrap ErrBadDigest: %v", err)
		}
		if _, again := reader.Read(make([]byte, 4)); !errors.Is(again, err) {
			t.Fatalf("second Read() error = %v, want the sticky error", again)
		}
	})
	t.Run("body shorter than content length", func(t *testing.T) {
		env := newTestEnv(t, "us-east-1")
		req := sdkRequest{method: http.MethodPut, target: "/b/k", body: string(data), payloadHash: good}.build(t)
		auth, _ := env.verifier.Authenticate(t.Context(), req)
		req.Body = io.NopCloser(io.MultiReader(strings.NewReader("short"), iotest.ErrReader(io.ErrUnexpectedEOF)))
		reader, _ := env.verifier.Body(req, auth)
		_, err := io.ReadAll(reader)
		requireReason(t, err, ReasonIncompleteBody)
	})
	t.Run("transport errors keep their type", func(t *testing.T) {
		env := newTestEnv(t, "us-east-1")
		req := sdkRequest{method: http.MethodPut, target: "/b/k", body: string(data), payloadHash: good}.build(t)
		auth, _ := env.verifier.Authenticate(t.Context(), req)
		boom := errors.New("connection reset")
		req.Body = io.NopCloser(iotest.ErrReader(boom))
		reader, _ := env.verifier.Body(req, auth)
		_, err := io.ReadAll(reader)
		var typed *Error
		if !errors.Is(err, boom) || errors.As(err, &typed) {
			t.Fatalf("ReadAll() error = %v", err)
		}
	})
	t.Run("get ignores the body", func(t *testing.T) {
		env := newTestEnv(t, "us-east-1")
		req := sdkRequest{method: http.MethodGet, target: "/b/k", payloadHash: emptyPayloadSHA256}.build(t)
		auth, err := env.verifier.Authenticate(t.Context(), req)
		if err != nil {
			t.Fatal(err)
		}
		req.Body = io.NopCloser(strings.NewReader("ignored body"))
		reader, _ := env.verifier.Body(req, auth)
		got, err := io.ReadAll(reader)
		if err != nil || len(got) != 0 {
			t.Fatalf("ReadAll() = %q, %v", got, err)
		}
	})
}

func TestStreamingSigned(t *testing.T) {
	data := patternBytes(200000)
	tests := []struct {
		name      string
		chunkSize int
	}{
		{name: "single chunk", chunkSize: len(data)},
		{name: "64 KiB chunks", chunkSize: 65536},
		{name: "uneven chunks", chunkSize: 7777},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newStreamingFixture(t, hashStreamingSigned, len(data), "")
			if fixture.auth.Mode != PayloadStreamingSigned || fixture.auth.DecodedLength != int64(len(data)) {
				t.Fatalf("Auth = %+v", fixture.auth)
			}
			encoded := encodeStream(paramsFromAuth(fixture.auth), splitChunks(data, test.chunkSize), "")
			got, _, err := fixture.read(t, encoded)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("read = %d bytes, %v", len(got), err)
			}
		})
	}
	t.Run("one byte reads", func(t *testing.T) {
		small := patternBytes(300)
		fixture := newStreamingFixture(t, hashStreamingSigned, len(small), "")
		encoded := encodeStream(paramsFromAuth(fixture.auth), splitChunks(small, 100), "")
		fixture.req.Body = io.NopCloser(iotest.OneByteReader(bytes.NewReader(encoded)))
		reader, err := fixture.env.verifier.Body(fixture.req, fixture.auth)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(iotest.OneByteReader(reader))
		if err != nil || !bytes.Equal(got, small) {
			t.Fatalf("read = %d bytes, %v", len(got), err)
		}
	})
	t.Run("empty object", func(t *testing.T) {
		fixture := newStreamingFixture(t, hashStreamingSigned, 0, "")
		got, _, err := fixture.read(t, encodeStream(paramsFromAuth(fixture.auth), nil, ""))
		if err != nil || len(got) != 0 {
			t.Fatalf("read = %d bytes, %v", len(got), err)
		}
	})
}

func TestAWSDocExampleChunkedUpload(t *testing.T) {
	example := loadDocExample(t, "awsdoc_chunked_upload.json")
	env := example.env(t)
	req := example.request(t)
	checkDocExampleCanonical(t, example, req, "content-encoding;content-length;host;x-amz-content-sha256;x-amz-date;x-amz-decoded-content-length;x-amz-storage-class", hashStreamingSigned, "")
	length, err := strconv.ParseInt(example.Headers["Content-Length"], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = length

	var body bytes.Buffer
	for _, chunk := range example.Chunks {
		fmt.Fprintf(&body, "%x;chunk-signature=%s\r\n", chunk.Size, chunk.Signature)
		body.WriteString(strings.Repeat(example.ChunkByte, chunk.Size))
		body.WriteString("\r\n")
	}
	if int64(body.Len()) != length {
		t.Fatalf("encoded body is %d bytes, the document says %d", body.Len(), length)
	}
	auth, err := env.verifier.Authenticate(t.Context(), req)
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if auth.Mode != PayloadStreamingSigned || auth.DecodedLength != 66560 {
		t.Fatalf("Auth = %+v", auth)
	}
	req.Body = io.NopCloser(bytes.NewReader(body.Bytes()))
	reader, err := env.verifier.Body(req, auth)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(reader)
	if err != nil || len(got) != 66560 || strings.Trim(string(got), example.ChunkByte) != "" {
		t.Fatalf("read = %d bytes, %v", len(got), err)
	}
}

func TestStreamingSignedChunkTampered(t *testing.T) {
	data := patternBytes(3000)
	fixture := newStreamingFixture(t, hashStreamingSigned, len(data), "")
	encoded := encodeStream(paramsFromAuth(fixture.auth), splitChunks(data, 1000), "")
	firstFrame := bytes.Index(encoded, []byte("\r\n")) + 2
	secondData := firstFrame + 1000 + 2 + bytes.Index(encoded[firstFrame+1002:], []byte("\r\n")) + 2

	tests := []struct {
		name         string
		mutate       func([]byte)
		wantReleased int
		reason       Reason
	}{
		{name: "data byte of second chunk", mutate: func(b []byte) { b[secondData+10] ^= 0x01 }, wantReleased: 1000, reason: ReasonSignature},
		{name: "signature of first chunk", mutate: func(b []byte) { b[strings.Index(string(b), "chunk-signature=")+20] ^= 0x01 }, wantReleased: 0, reason: ReasonSignature},
		{name: "data byte of first chunk", mutate: func(b []byte) { b[firstFrame+5] ^= 0x01 }, wantReleased: 0, reason: ReasonSignature},
		{name: "non hex signature", mutate: func(b []byte) { b[strings.Index(string(b), "chunk-signature=")+17] = 'Z' }, wantReleased: 0, reason: ReasonChunkFraming},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mutated := bytes.Clone(encoded)
			test.mutate(mutated)
			got, reader, err := fixture.read(t, mutated)
			requireReason(t, err, test.reason)
			if len(got) != test.wantReleased {
				t.Fatalf("released %d bytes before the failure, want %d", len(got), test.wantReleased)
			}
			if _, again := reader.Read(make([]byte, 1)); !errors.Is(again, err) {
				t.Fatalf("second Read() error = %v", again)
			}
		})
	}
	t.Run("signature chain across chunks", func(t *testing.T) {
		swapped := encodeStream(paramsFromAuth(fixture.auth), [][]byte{data[1000:2000], data[:1000], data[2000:]}, "")
		got, _, err := fixture.read(t, swapped)
		if err != nil || len(got) != 3000 {
			t.Fatalf("a correctly chained stream failed: %d bytes, %v", len(got), err)
		}
		first := encodeStream(paramsFromAuth(fixture.auth), splitChunks(data, 1000), "")
		frame := len("3e8;chunk-signature=") + signatureHexLength + 2 + 1000 + 2
		reordered := append([]byte(nil), first[frame:2*frame]...)
		reordered = append(reordered, first[:frame]...)
		reordered = append(reordered, first[2*frame:]...)
		_, _, err = fixture.read(t, reordered)
		requireReason(t, err, ReasonSignature)
	})
}

func TestStreamingSignedTrailer(t *testing.T) {
	data := patternBytes(5000)
	fixture := newStreamingFixture(t, hashStreamingSignedTrailer, len(data), "x-amz-checksum-crc32")
	if fixture.auth.Mode != PayloadStreamingSignedTrailer || fixture.auth.Trailer != "x-amz-checksum-crc32" {
		t.Fatalf("Auth = %+v", fixture.auth)
	}
	params := paramsFromAuth(fixture.auth)
	chunks := splitChunks(data, 2048)

	t.Run("valid", func(t *testing.T) {
		got, reader, err := fixture.read(t, encodeStream(params, chunks, crc32Base64(data)))
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("read = %d bytes, %v", len(got), err)
		}
		if algorithm, value := reader.Checksum(); algorithm != "crc32" || value != crc32Base64(data) {
			t.Fatalf("Checksum() = %q, %q", algorithm, value)
		}
	})
	t.Run("trailer signature tampered", func(t *testing.T) {
		encoded := encodeStream(params, chunks, crc32Base64(data))
		at := bytes.LastIndex(encoded, []byte("x-amz-trailer-signature:")) + len("x-amz-trailer-signature:") + 3
		encoded[at] ^= 0x01
		_, _, err := fixture.read(t, encoded)
		requireReason(t, err, ReasonSignature)
	})
	t.Run("trailer value changed after signing", func(t *testing.T) {
		encoded := encodeStream(params, chunks, crc32Base64(data))
		at := bytes.Index(encoded, []byte("x-amz-checksum-crc32:")) + len("x-amz-checksum-crc32:")
		encoded[at] = 'A' + (encoded[at]-'A'+1)%26
		_, _, err := fixture.read(t, encoded)
		requireReason(t, err, ReasonSignature)
	})
	t.Run("correctly signed wrong checksum", func(t *testing.T) {
		_, _, err := fixture.read(t, encodeStream(params, chunks, crc32Base64([]byte("not the data"))))
		typed := requireReason(t, err, ReasonChecksumMismatch)
		if typed.Algorithm != "crc32" {
			t.Fatalf("Error.Algorithm = %q", typed.Algorithm)
		}
	})
	t.Run("missing trailer signature", func(t *testing.T) {
		encoded := encodeStream(params, chunks, crc32Base64(data))
		at := bytes.LastIndex(encoded, []byte("x-amz-trailer-signature:"))
		_, _, err := fixture.read(t, append(encoded[:at:at], '\r', '\n'))
		requireReason(t, err, ReasonChunkFraming)
	})
	t.Run("trailer carries a different header", func(t *testing.T) {
		other := params
		other.trailerName = "x-amz-checksum-sha1"
		_, _, err := fixture.read(t, encodeStream(other, chunks, base64.StdEncoding.EncodeToString(make([]byte, 20))))
		requireReason(t, err, ReasonChunkFraming)
	})
}

func TestStreamingUnsignedTrailerCRC32(t *testing.T) {
	data := patternBytes(70000)
	fixture := newStreamingFixture(t, hashStreamingUnsignedTrailer, len(data), "x-amz-checksum-crc32")
	if fixture.auth.Mode != PayloadStreamingUnsignedTrailer {
		t.Fatalf("Auth = %+v", fixture.auth)
	}
	params := paramsFromAuth(fixture.auth)
	chunks := splitChunks(data, 65536)

	t.Run("valid", func(t *testing.T) {
		got, reader, err := fixture.read(t, encodeStream(params, chunks, crc32Base64(data)))
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("read = %d bytes, %v", len(got), err)
		}
		if algorithm, value := reader.Checksum(); algorithm != "crc32" || value != crc32Base64(data) {
			t.Fatalf("Checksum() = %q, %q", algorithm, value)
		}
	})
	t.Run("chunk extensions are ignored", func(t *testing.T) {
		encoded := bytes.Replace(encodeStream(params, chunks, crc32Base64(data)), []byte("\r\n"), []byte(";ext=1\r\n"), 1)
		got, _, err := fixture.read(t, encoded)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("read = %d bytes, %v", len(got), err)
		}
	})
	t.Run("mixed case trailer name", func(t *testing.T) {
		encoded := bytes.Replace(encodeStream(params, chunks, crc32Base64(data)), []byte("x-amz-checksum-crc32:"), []byte("X-Amz-Checksum-CRC32: "), 1)
		got, _, err := fixture.read(t, encoded)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("read = %d bytes, %v", len(got), err)
		}
	})
	t.Run("trailer missing", func(t *testing.T) {
		encoded := []byte("0\r\n\r\n")
		empty := newStreamingFixture(t, hashStreamingUnsignedTrailer, 0, "x-amz-checksum-crc32")
		_, _, err := empty.read(t, encoded)
		requireReason(t, err, ReasonChunkFraming)
	})
	t.Run("extra trailer header", func(t *testing.T) {
		encoded := encodeStream(params, chunks, crc32Base64(data))
		encoded = append(encoded[:len(encoded)-2], []byte("x-amz-checksum-sha1:AAAA\r\n\r\n")...)
		_, _, err := fixture.read(t, encoded)
		requireReason(t, err, ReasonChunkFraming)
	})
	t.Run("trailer over 16 KiB", func(t *testing.T) {
		encoded := encodeStream(params, chunks, crc32Base64(data))
		encoded = append(encoded[:len(encoded)-2], []byte("x-pad:"+strings.Repeat("a", 3000)+"\r\n\r\n")...)
		_, _, err := fixture.read(t, encoded)
		requireReason(t, err, ReasonChunkFraming)
		big := encodeStream(params, chunks, crc32Base64(data))
		var padded bytes.Buffer
		padded.Write(big[:len(big)-2])
		for range 6 {
			padded.WriteString("x-pad:" + strings.Repeat("a", 3000) + "\r\n")
		}
		padded.WriteString("\r\n")
		_, _, err = fixture.read(t, padded.Bytes())
		requireReason(t, err, ReasonChunkFraming)
	})
}

func TestTrailerChecksumAlgorithms(t *testing.T) {
	data := patternBytes(9000)
	sha1Sum := sha1.Sum(data)
	sha256Sum := sha256.Sum256(data)
	crc32cSum := crc32.Checksum(data, crc32.MakeTable(crc32.Castagnoli))
	crc64Sum := crc64.Checksum(data, crc64.MakeTable(0x9a6c9329ac4bc9b5))
	crc64Bytes := make([]byte, 8)
	for i := range crc64Bytes {
		crc64Bytes[i] = byte(crc64Sum >> (56 - 8*i))
	}
	tests := []struct {
		name  string
		value string
	}{
		{name: "crc32", value: crc32Base64(data)},
		{name: "crc32c", value: base64.StdEncoding.EncodeToString([]byte{byte(crc32cSum >> 24), byte(crc32cSum >> 16), byte(crc32cSum >> 8), byte(crc32cSum)})},
		{name: "crc64nvme", value: base64.StdEncoding.EncodeToString(crc64Bytes)},
		{name: "sha1", value: base64.StdEncoding.EncodeToString(sha1Sum[:])},
		{name: "sha256", value: base64.StdEncoding.EncodeToString(sha256Sum[:])},
	}
	for _, test := range tests {
		for _, signed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s signed=%v", test.name, signed), func(t *testing.T) {
				payloadHash := hashStreamingUnsignedTrailer
				if signed {
					payloadHash = hashStreamingSignedTrailer
				}
				fixture := newStreamingFixture(t, payloadHash, len(data), "x-amz-checksum-"+test.name)
				got, reader, err := fixture.read(t, encodeStream(paramsFromAuth(fixture.auth), splitChunks(data, 4096), test.value))
				if err != nil || !bytes.Equal(got, data) {
					t.Fatalf("read = %d bytes, %v", len(got), err)
				}
				if algorithm, value := reader.Checksum(); algorithm != test.name || value != test.value {
					t.Fatalf("Checksum() = %q, %q", algorithm, value)
				}
			})
		}
	}
	t.Run("crc64nvme known vector", func(t *testing.T) {
		sum := crc64.Checksum([]byte("123456789"), crc64.MakeTable(0x9a6c9329ac4bc9b5))
		if sum != 0xae8b14860a799888 {
			t.Fatalf("crc64nvme(123456789) = %#x", sum)
		}
	})
}

func TestTrailerMismatchBadDigest(t *testing.T) {
	data := patternBytes(1000)
	for _, algorithm := range checksumAlgorithms() {
		t.Run(algorithm.name, func(t *testing.T) {
			fixture := newStreamingFixture(t, hashStreamingUnsignedTrailer, len(data), "x-amz-checksum-"+algorithm.name)
			wrong := base64.StdEncoding.EncodeToString(make([]byte, algorithm.size))
			_, _, err := fixture.read(t, encodeStream(paramsFromAuth(fixture.auth), splitChunks(data, 500), wrong))
			typed := requireReason(t, err, ReasonChecksumMismatch)
			if typed.Algorithm != algorithm.name || !errors.Is(err, errs.ErrBadDigest) {
				t.Fatalf("Error = %+v", typed)
			}
		})
	}
	t.Run("malformed trailer value", func(t *testing.T) {
		fixture := newStreamingFixture(t, hashStreamingUnsignedTrailer, len(data), "x-amz-checksum-crc32")
		_, _, err := fixture.read(t, encodeStream(paramsFromAuth(fixture.auth), splitChunks(data, 500), "not base64!"))
		requireReason(t, err, ReasonChecksumMalformed)
	})
	t.Run("wrong length trailer value", func(t *testing.T) {
		fixture := newStreamingFixture(t, hashStreamingUnsignedTrailer, len(data), "x-amz-checksum-crc32")
		_, _, err := fixture.read(t, encodeStream(paramsFromAuth(fixture.auth), splitChunks(data, 500), base64.StdEncoding.EncodeToString(make([]byte, 5))))
		requireReason(t, err, ReasonChecksumMalformed)
	})
}

func TestDecodedLengthMismatch(t *testing.T) {
	data := patternBytes(3000)
	tests := []struct {
		name        string
		payloadHash string
		trailer     string
		declared    int
	}{
		{name: "signed declared too short", payloadHash: hashStreamingSigned, declared: 2999},
		{name: "signed declared too long", payloadHash: hashStreamingSigned, declared: 3001},
		{name: "unsigned trailer declared too short", payloadHash: hashStreamingUnsignedTrailer, trailer: "x-amz-checksum-crc32", declared: 10},
		{name: "unsigned trailer declared too long", payloadHash: hashStreamingUnsignedTrailer, trailer: "x-amz-checksum-crc32", declared: 3001},
		{name: "signed trailer declared too long", payloadHash: hashStreamingSignedTrailer, trailer: "x-amz-checksum-crc32", declared: 4000},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newStreamingFixture(t, test.payloadHash, test.declared, test.trailer)
			params := paramsFromAuth(fixture.auth)
			_, _, err := fixture.read(t, encodeStream(params, splitChunks(data, 1000), crc32Base64(data)))
			requireReason(t, err, ReasonIncompleteBody)
		})
	}
}

func TestStreamingHeaderRules(t *testing.T) {
	t.Run("trailer mode without trailer header", func(t *testing.T) {
		env := newTestEnv(t, "us-east-1")
		req := sdkRequest{method: http.MethodPut, target: "/b/k", payloadHash: hashStreamingUnsignedTrailer, headers: map[string]string{"X-Amz-Decoded-Content-Length": "1"}}.build(t)
		_, err := env.verifier.Authenticate(t.Context(), req)
		requireReason(t, err, ReasonChunkFraming)
	})
	t.Run("trailer header on non trailer mode", func(t *testing.T) {
		env := newTestEnv(t, "us-east-1")
		req := sdkRequest{method: http.MethodPut, target: "/b/k", payloadHash: hashStreamingSigned, headers: map[string]string{"X-Amz-Decoded-Content-Length": "1", "X-Amz-Trailer": "x-amz-checksum-crc32"}}.build(t)
		_, err := env.verifier.Authenticate(t.Context(), req)
		requireReason(t, err, ReasonChunkFraming)
	})
	t.Run("trailer header on unsigned payload", func(t *testing.T) {
		env := newTestEnv(t, "us-east-1")
		req := sdkRequest{method: http.MethodPut, target: "/b/k", body: "x", headers: map[string]string{"X-Amz-Trailer": "x-amz-checksum-crc32"}}.build(t)
		_, err := env.verifier.Authenticate(t.Context(), req)
		requireReason(t, err, ReasonChunkFraming)
	})
	t.Run("unknown trailer name", func(t *testing.T) {
		env := newTestEnv(t, "us-east-1")
		req := sdkRequest{method: http.MethodPut, target: "/b/k", payloadHash: hashStreamingUnsignedTrailer, headers: map[string]string{"X-Amz-Decoded-Content-Length": "1", "X-Amz-Trailer": "x-amz-checksum-md5"}}.build(t)
		_, err := env.verifier.Authenticate(t.Context(), req)
		requireReason(t, err, ReasonChecksumMalformed)
	})
	t.Run("two trailer names", func(t *testing.T) {
		env := newTestEnv(t, "us-east-1")
		req := sdkRequest{method: http.MethodPut, target: "/b/k", payloadHash: hashStreamingUnsignedTrailer, headers: map[string]string{"X-Amz-Decoded-Content-Length": "1", "X-Amz-Trailer": "x-amz-checksum-crc32,x-amz-checksum-sha1"}}.build(t)
		_, err := env.verifier.Authenticate(t.Context(), req)
		requireReason(t, err, ReasonChecksumMalformed)
	})
	t.Run("decoded length absent", func(t *testing.T) {
		fixture := newStreamingFixture(t, hashStreamingSigned, -1, "")
		if fixture.auth.DecodedLength != -1 {
			t.Fatalf("Auth.DecodedLength = %d", fixture.auth.DecodedLength)
		}
		fixture.req.Body = io.NopCloser(strings.NewReader(""))
		if _, err := fixture.env.verifier.Body(fixture.req, fixture.auth); err == nil {
			t.Fatal("Body() accepted a streaming request with no decoded length")
		}
	})
	t.Run("decoded length invalid", func(t *testing.T) {
		env := newTestEnv(t, "us-east-1")
		req := sdkRequest{method: http.MethodPut, target: "/b/k", payloadHash: hashStreamingSigned, headers: map[string]string{"X-Amz-Decoded-Content-Length": "-4"}}.build(t)
		_, err := env.verifier.Authenticate(t.Context(), req)
		requireReason(t, err, ReasonChunkFraming)
	})
	t.Run("trailer checksum and header checksum together", func(t *testing.T) {
		env := newTestEnv(t, "us-east-1")
		req := sdkRequest{method: http.MethodPut, target: "/b/k", payloadHash: hashStreamingUnsignedTrailer, headers: map[string]string{
			"X-Amz-Decoded-Content-Length": "1", "X-Amz-Trailer": "x-amz-checksum-crc32", "X-Amz-Checksum-Sha1": base64.StdEncoding.EncodeToString(make([]byte, 20)),
		}}.build(t)
		auth, err := env.verifier.Authenticate(t.Context(), req)
		if err != nil {
			t.Fatal(err)
		}
		_, err = env.verifier.Body(req, auth)
		requireReason(t, err, ReasonChecksumMalformed)
	})
}

func TestChunkHeaderTooLong(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "signed header over 4 KiB", body: strings.Repeat("a", 5000) + ";chunk-signature=" + strings.Repeat("0", 64) + "\r\n"},
		{name: "signed header with no newline", body: strings.Repeat("a", 200000)},
		{name: "more than sixteen hex digits", body: "00000000000000001;chunk-signature=" + strings.Repeat("0", 64) + "\r\n"},
		{name: "chunk over 16 MiB", body: "1000001;chunk-signature=" + strings.Repeat("0", 64) + "\r\n"},
		{name: "empty size", body: ";chunk-signature=" + strings.Repeat("0", 64) + "\r\n"},
		{name: "non hex size", body: "zz;chunk-signature=" + strings.Repeat("0", 64) + "\r\n"},
		{name: "signed header missing signature", body: "10\r\n"},
		{name: "bare newline", body: "10;chunk-signature=" + strings.Repeat("0", 64) + "\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newStreamingFixture(t, hashStreamingSigned, 16, "")
			_, _, err := fixture.read(t, []byte(test.body))
			requireReason(t, err, ReasonChunkFraming)
		})
	}
	t.Run("unsigned header over 4 KiB", func(t *testing.T) {
		fixture := newStreamingFixture(t, hashStreamingUnsignedTrailer, 16, "x-amz-checksum-crc32")
		_, _, err := fixture.read(t, []byte("10;"+strings.Repeat("e", 5000)+"\r\n"))
		requireReason(t, err, ReasonChunkFraming)
	})
}

func TestTruncatedChunkedBody(t *testing.T) {
	data := patternBytes(2500)
	tests := []struct {
		name        string
		payloadHash string
		trailer     string
	}{
		{name: "signed", payloadHash: hashStreamingSigned},
		{name: "signed trailer", payloadHash: hashStreamingSignedTrailer, trailer: "x-amz-checksum-crc32"},
		{name: "unsigned trailer", payloadHash: hashStreamingUnsignedTrailer, trailer: "x-amz-checksum-crc32"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newStreamingFixture(t, test.payloadHash, len(data), test.trailer)
			encoded := encodeStream(paramsFromAuth(fixture.auth), splitChunks(data, 1000), crc32Base64(data))
			if _, _, err := fixture.read(t, encoded); err != nil {
				t.Fatalf("full body failed: %v", err)
			}
			for cut := range len(encoded) {
				_, _, err := fixture.read(t, encoded[:cut])
				if err == nil {
					t.Fatalf("body cut at %d of %d decoded without error", cut, len(encoded))
				}
				if typed := asError(t, err); typed.Reason != ReasonIncompleteBody && typed.Reason != ReasonChunkFraming {
					t.Fatalf("cut %d: reason = %d", cut, typed.Reason)
				}
			}
		})
	}
}

func TestChunkedTrailingBytesRejected(t *testing.T) {
	data := patternBytes(100)
	tests := []struct {
		name        string
		payloadHash string
		trailer     string
	}{
		{name: "signed", payloadHash: hashStreamingSigned},
		{name: "signed trailer", payloadHash: hashStreamingSignedTrailer, trailer: "x-amz-checksum-crc32"},
		{name: "unsigned trailer", payloadHash: hashStreamingUnsignedTrailer, trailer: "x-amz-checksum-crc32"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newStreamingFixture(t, test.payloadHash, len(data), test.trailer)
			encoded := encodeStream(paramsFromAuth(fixture.auth), splitChunks(data, 50), crc32Base64(data))
			_, _, err := fixture.read(t, append(encoded, 'x'))
			requireReason(t, err, ReasonChunkFraming)
		})
	}
}

func TestChunkDecoderRejectsMissingTerminators(t *testing.T) {
	data := patternBytes(10)
	fixture := newStreamingFixture(t, hashStreamingUnsignedTrailer, len(data), "x-amz-checksum-crc32")
	params := paramsFromAuth(fixture.auth)
	good := encodeStream(params, [][]byte{data}, crc32Base64(data))
	broken := bytes.Replace(good, append(data, '\r', '\n'), append(data, 'X', 'Y'), 1)
	_, _, err := fixture.read(t, broken)
	requireReason(t, err, ReasonChunkFraming)
}

func TestNewChunkDecoderBoundsMemoryOnLyingHeader(t *testing.T) {
	fixture := newStreamingFixture(t, hashStreamingSigned, 1<<24, "")
	lie := "1000000;chunk-signature=" + strings.Repeat("0", 64) + "\r\nabc"
	_, _, err := fixture.read(t, []byte(lie))
	requireReason(t, err, ReasonIncompleteBody)
}

func FuzzChunkDecoder(f *testing.F) {
	signingKey := deriveSigningKey([]byte(testSecret), "20261113", "us-east-1")
	base := chunkParams{signingKey: signingKey, seed: strings.Repeat("a", 64), requestTime: baseTime, region: "us-east-1"}
	modes := func(mode byte, decoded int64) chunkParams {
		params := base
		params.decodedLength = decoded
		switch mode % 3 {
		case 0:
			params.signedChunks = true
		case 1:
			params.signedChunks, params.signedTrailer, params.trailerName = true, true, "x-amz-checksum-crc32"
		default:
			params.trailerName = "x-amz-checksum-crc32"
		}
		return params
	}
	payload := patternBytes(300)
	for mode := range byte(3) {
		params := modes(mode, int64(len(payload)))
		f.Add(encodeStream(params, splitChunks(payload, 128), crc32Base64(payload)), mode, int64(len(payload)))
	}
	f.Add([]byte("0\r\n\r\n"), byte(2), int64(0))
	f.Add([]byte("5;chunk-signature=zz\r\nhello\r\n"), byte(0), int64(5))
	f.Add([]byte(strings.Repeat("f", 40)), byte(2), int64(-1))
	f.Fuzz(func(t *testing.T, data []byte, mode byte, decoded int64) {
		params := modes(mode, decoded)
		out, err := io.ReadAll(newChunkDecoder(bytes.NewReader(data), params))
		if err == nil {
			if decoded >= 0 && int64(len(out)) != decoded {
				t.Fatalf("decoded %d bytes without error, declared %d", len(out), decoded)
			}
			return
		}
		var typed *Error
		if !errors.As(err, &typed) {
			t.Fatalf("unexpected error type %T: %v", err, err)
		}
		if decoded >= 0 && int64(len(out)) > decoded {
			t.Fatalf("released %d bytes over the declared %d", len(out), decoded)
		}
	})
}
