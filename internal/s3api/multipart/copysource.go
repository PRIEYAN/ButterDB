package multipart

import (
	"context"
	"errors"
	"io"
	"net/url"
	"strconv"
	"strings"

	"github.com/PRIEYAN/ButterDB/internal/readpath/objectreader"
	"github.com/PRIEYAN/ButterDB/internal/s3api/s3err"
)

const (
	copySourceHeader     = "X-Amz-Copy-Source"
	copySourceRangeName  = "X-Amz-Copy-Source-Range"
	copySourceRangeText  = "The x-amz-copy-source-range value must be of the form bytes=first-last where first and last are the zero-based offsets of the first and last bytes to copy"
	copySourceInvalid    = "Copy Source must mention the source bucket and key: sourcebucket/sourcekey"
	invalidVersionIDText = "Invalid version id specified"
)

type copySource struct {
	bucket string
	key    string
}

func parseCopySource(raw string) (copySource, error) {
	location, query, hasQuery := strings.Cut(raw, "?")
	if hasQuery {
		values, err := url.ParseQuery(query)
		if err != nil {
			return copySource{}, s3err.New(s3err.CodeInvalidArgument, copySourceInvalid)
		}
		if versionID := values.Get("versionId"); versionID != "" && versionID != "null" {
			return copySource{}, s3err.New(s3err.CodeInvalidArgument, invalidVersionIDText)
		}
	}
	decoded, err := url.PathUnescape(strings.TrimPrefix(location, "/"))
	if err != nil {
		return copySource{}, s3err.New(s3err.CodeInvalidArgument, copySourceInvalid)
	}
	bucketName, key, found := strings.Cut(decoded, "/")
	if !found || bucketName == "" || key == "" {
		return copySource{}, s3err.New(s3err.CodeInvalidArgument, copySourceInvalid)
	}
	return copySource{bucket: bucketName, key: key}, nil
}

type byteSpan struct {
	start  int64
	length int64
}

func parseCopyRange(raw string, sourceSize int64) (byteSpan, error) {
	if raw == "" {
		return byteSpan{start: 0, length: sourceSize}, nil
	}
	spec, ok := strings.CutPrefix(raw, "bytes=")
	if !ok {
		return byteSpan{}, invalidCopyRange(raw)
	}
	firstText, lastText, ok := strings.Cut(spec, "-")
	if !ok {
		return byteSpan{}, invalidCopyRange(raw)
	}
	first, firstOK := parseOffset(firstText)
	last, lastOK := parseOffset(lastText)
	if !firstOK || !lastOK || first > last || last >= sourceSize {
		return byteSpan{}, invalidCopyRange(raw)
	}
	return byteSpan{start: first, length: last - first + 1}, nil
}

func parseOffset(text string) (int64, bool) {
	if text == "" || strings.TrimLeft(text, "0123456789") != "" {
		return 0, false
	}
	value, err := strconv.ParseInt(text, 10, 64)
	return value, err == nil
}

func invalidCopyRange(raw string) error {
	api := s3err.New(s3err.CodeInvalidArgument, copySourceRangeText)
	api.Extra = []s3err.Field{
		{Name: "ArgumentName", Value: strings.ToLower(copySourceRangeName)},
		{Name: "ArgumentValue", Value: raw},
	}
	return api
}

type spanReader struct {
	ctx       context.Context
	source    objectreader.Opened
	offset    int64
	remaining int64
}

func newSpanReader(ctx context.Context, source objectreader.Opened, span byteSpan) *spanReader {
	return &spanReader{ctx: ctx, source: source, offset: span.start, remaining: span.length}
}

func (s *spanReader) Read(p []byte) (int, error) {
	if s.remaining == 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > s.remaining {
		p = p[:s.remaining]
	}
	n, err := s.source.ReadAt(s.ctx, p, s.offset)
	s.offset += int64(n)
	s.remaining -= int64(n)
	if errors.Is(err, io.EOF) {
		if s.remaining > 0 && n == 0 {
			return 0, io.ErrUnexpectedEOF
		}
		err = nil
	}
	return n, err
}
