package multipart

import (
	"crypto/md5"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/s3api/s3err"
)

const contentMD5Bytes = 16

func parseContentMD5(header http.Header) ([]byte, error) {
	raw := header.Get("Content-MD5")
	if raw == "" {
		return nil, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(decoded) != contentMD5Bytes {
		return nil, s3err.New(s3err.CodeInvalidDigest, "")
	}
	return decoded, nil
}

type md5VerifyingReader struct {
	src  io.Reader
	hash hash.Hash
	want []byte
	err  error
}

func newMD5VerifyingReader(src io.Reader, want []byte) *md5VerifyingReader {
	return &md5VerifyingReader{src: src, hash: md5.New(), want: want}
}

func (v *md5VerifyingReader) Read(p []byte) (int, error) {
	if v.err != nil {
		return 0, v.err
	}
	n, err := v.src.Read(p)
	v.hash.Write(p[:n])
	if errors.Is(err, io.EOF) && subtle.ConstantTimeCompare(v.hash.Sum(nil), v.want) != 1 {
		v.err = fmt.Errorf("multipart: content-md5 mismatch: %w", errs.ErrBadDigest)
		return n, v.err
	}
	return n, err
}

type idleDeadlineBody struct {
	src     io.Reader
	control *http.ResponseController
	clock   clock.Clock
	timeout time.Duration
}

func newIdleDeadlineBody(src io.Reader, w http.ResponseWriter, c clock.Clock, timeout time.Duration) io.Reader {
	if timeout <= 0 {
		return src
	}
	return &idleDeadlineBody{src: src, control: http.NewResponseController(w), clock: c, timeout: timeout}
}

func (b *idleDeadlineBody) Read(p []byte) (int, error) {
	_ = b.control.SetReadDeadline(b.clock.Now().Add(b.timeout))
	n, err := b.src.Read(p)
	if err != nil {
		_ = b.control.SetReadDeadline(time.Time{})
	}
	return n, err
}
