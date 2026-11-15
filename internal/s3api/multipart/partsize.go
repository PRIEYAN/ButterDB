package multipart

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/PRIEYAN/ButterDB/internal/access/sigv4"
	"github.com/PRIEYAN/ButterDB/internal/crypto/format"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/limits"
	"github.com/PRIEYAN/ButterDB/internal/s3api/s3err"
)

const stagePageSize = 1000

func parsePartNumber(raw string) (int, error) {
	number, err := strconv.Atoi(raw)
	if err != nil || raw == "" || raw[0] == '+' || raw[0] == '-' || number < 1 || number > limits.MaxMultipartParts {
		api := s3err.New(s3err.CodeInvalidArgument, "Part number must be an integer between 1 and 10000, inclusive")
		api.Extra = []s3err.Field{
			{Name: "ArgumentName", Value: partNumberQuery},
			{Name: "ArgumentValue", Value: raw},
		}
		return 0, api
	}
	return number, nil
}

func isStreamingMode(mode sigv4.PayloadMode) bool {
	switch mode {
	case sigv4.PayloadStreamingSigned, sigv4.PayloadStreamingSignedTrailer, sigv4.PayloadStreamingUnsignedTrailer:
		return true
	}
	return false
}

func declaredSize(r *http.Request, a sigv4.Auth) (int64, error) {
	size := r.ContentLength
	if isStreamingMode(a.Mode) {
		size = a.DecodedLength
	}
	if size < 0 {
		return 0, s3err.New(s3err.CodeMissingContentLength, "")
	}
	return size, nil
}

func exceedsStoredCeiling(encrypted bool, plaintext int64) bool {
	if encrypted {
		return plaintext > limits.MaxEncryptedPlaintextBytes || format.CiphertextSize(plaintext) > limits.MaxStoredObjectBytes
	}
	return plaintext > limits.MaxStoredObjectBytes
}

func entityTooLarge(proposed int64) error {
	api := s3err.Map(errs.ErrObjectTooLarge)
	api.Extra = append([]s3err.Field{{Name: "ProposedSize", Value: strconv.FormatInt(proposed, 10)}}, api.Extra...)
	return api
}

func (h *Handlers) checkRunningTotal(ctx context.Context, record UploadRecord, partNumber int, size int64) error {
	if exceedsStoredCeiling(record.Encrypted, size) {
		return entityTooLarge(size)
	}
	staged, err := h.listAllParts(ctx, record.ID)
	if err != nil {
		return err
	}
	total := size
	for _, part := range staged {
		if part.Number != partNumber {
			total += part.Size
		}
	}
	if exceedsStoredCeiling(record.Encrypted, total) {
		return entityTooLarge(total)
	}
	return nil
}

func (h *Handlers) listAllParts(ctx context.Context, uploadID string) ([]stagedPart, error) {
	var all []stagedPart
	marker := 0
	for {
		page, err := h.deps.Stage.ListParts(ctx, uploadID, marker, stagePageSize)
		if err != nil {
			return nil, fmt.Errorf("multipart: list staged parts: %w", err)
		}
		for _, part := range page.Parts {
			all = append(all, stagedPart{Number: part.Number, Size: part.Size, ETag: part.ETag, Checksum: part.Checksum})
		}
		if !page.Truncated || len(page.Parts) == 0 {
			return all, nil
		}
		marker = page.Parts[len(page.Parts)-1].Number
	}
}

type stagedPart struct {
	Number   int
	Size     int64
	ETag     string
	Checksum string
}
