package multipart

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/PRIEYAN/ButterDB/internal/domain/limits"
	"github.com/PRIEYAN/ButterDB/internal/ingest/multipartstage"
	"github.com/PRIEYAN/ButterDB/internal/s3api/s3err"
)

func listedChecksum(part completePart) string {
	for _, value := range []string{part.ChecksumCRC32, part.ChecksumCRC32C, part.ChecksumCRC64NVME, part.ChecksumSHA1, part.ChecksumSHA256} {
		if value != "" {
			return value
		}
	}
	return ""
}

func normalizeListedETag(etag string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(etag), `"`))
}

func parseWriteConditions(header http.Header) (multipartstage.Conditions, error) {
	var conditions multipartstage.Conditions
	switch value := strings.TrimSpace(header.Get("If-None-Match")); value {
	case "":
	case "*":
		conditions.IfNoneMatchAny = true
	default:
		return multipartstage.Conditions{}, s3err.New(s3err.CodeInvalidArgument, "If-None-Match accepts only *")
	}
	conditions.IfMatch = strings.Trim(strings.TrimPrefix(strings.TrimSpace(header.Get("If-Match")), "W/"), `"`)
	return conditions, nil
}

func requireAscending(listed []completePart) error {
	for i := 1; i < len(listed); i++ {
		if listed[i].PartNumber <= listed[i-1].PartNumber {
			return s3err.New(s3err.CodeInvalidPartOrder, "")
		}
	}
	return nil
}

func stagedByNumber(staged []stagedPart) map[int]stagedPart {
	byNumber := make(map[int]stagedPart, len(staged))
	for _, part := range staged {
		byNumber[part.Number] = part
	}
	return byNumber
}

func validateListed(record UploadRecord, listed []completePart, staged []stagedPart) error {
	if len(listed) > limits.MaxMultipartParts {
		return s3err.New(s3err.CodeInvalidPart, "")
	}
	byNumber := stagedByNumber(staged)
	var plaintext int64
	for i, want := range listed {
		have, ok := byNumber[want.PartNumber]
		if !ok || normalizeListedETag(want.ETag) != have.ETag {
			return invalidPart(want)
		}
		if recorded := have.Checksum; recorded != "" && listedChecksum(want) != "" && listedChecksum(want) != recorded {
			return invalidPart(want)
		}
		if i < len(listed)-1 && have.Size < limits.MinMultipartPartBytes {
			return entityTooSmall(want, have.Size)
		}
		plaintext += have.Size
	}
	if exceedsStoredCeiling(record.Encrypted, plaintext) {
		return entityTooLarge(plaintext)
	}
	return nil
}

func invalidPart(part completePart) error {
	api := s3err.New(s3err.CodeInvalidPart, "")
	api.Extra = []s3err.Field{
		{Name: "PartNumber", Value: strconv.Itoa(part.PartNumber)},
		{Name: "ETag", Value: part.ETag},
	}
	return api
}

func entityTooSmall(part completePart, size int64) error {
	api := s3err.New(s3err.CodeEntityTooSmall, "")
	api.Extra = []s3err.Field{
		{Name: "ProposedSize", Value: strconv.FormatInt(size, 10)},
		{Name: "MinSizeAllowed", Value: strconv.Itoa(limits.MinMultipartPartBytes)},
		{Name: "PartNumber", Value: strconv.Itoa(part.PartNumber)},
		{Name: "ETag", Value: part.ETag},
	}
	return api
}

func completedPartsOf(listed []completePart, staged map[int]stagedPart) []multipartstage.CompletedPart {
	parts := make([]multipartstage.CompletedPart, 0, len(listed))
	for _, part := range listed {
		completed := multipartstage.CompletedPart{Number: part.PartNumber, ETag: part.ETag}
		recordedChecksum := staged[part.PartNumber].Checksum
		if recordedChecksum != "" {
			completed.Checksum = listedChecksum(part)
		}
		parts = append(parts, completed)
	}
	return parts
}
