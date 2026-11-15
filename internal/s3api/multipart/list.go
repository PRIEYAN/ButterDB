package multipart

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/PRIEYAN/ButterDB/internal/access/sigv4"
	"github.com/PRIEYAN/ButterDB/internal/domain/access"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/ingest/multipartstage"
	"github.com/PRIEYAN/ButterDB/internal/s3api/router"
	"github.com/PRIEYAN/ButterDB/internal/s3api/s3err"
	"github.com/PRIEYAN/ButterDB/internal/s3api/xmlcodec"
)

const (
	maxListEntries = 1000
	urlEncoding    = "url"
)

func (h *Handlers) ListParts(w http.ResponseWriter, r *http.Request, rt router.Route, a sigv4.Auth) {
	if _, err := h.authorize(r, rt, a, access.ActionList); err != nil {
		h.fail(w, r, rt, err)
		return
	}
	query := r.URL.Query()
	maxParts, err := parseListLimit(query, "max-parts")
	if err != nil {
		h.fail(w, r, rt, err)
		return
	}
	marker, err := parseNonNegative(query, "part-number-marker")
	if err != nil {
		h.fail(w, r, rt, err)
		return
	}
	record, err := h.lookupRequestedUpload(r, rt)
	if err == nil && record.State != stateOpen && record.State != stateCommitting {
		err = errs.ErrNoSuchUpload
	}
	if err != nil {
		h.fail(w, r, rt, err)
		return
	}
	page, err := h.deps.Stage.ListParts(r.Context(), record.ID, marker, maxParts)
	if err != nil {
		h.fail(w, r, rt, err)
		return
	}
	h.respond(w, r, rt, h.listPartsResult(rt, a, record, page, marker, maxParts))
}

func parseListLimit(query url.Values, name string) (int, error) {
	raw := query.Get(name)
	if raw == "" {
		return maxListEntries, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return 0, invalidQueryArgument(name, raw)
	}
	return min(value, maxListEntries), nil
}

func parseNonNegative(query url.Values, name string) (int, error) {
	raw := query.Get(name)
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, invalidQueryArgument(name, raw)
	}
	return value, nil
}

func invalidQueryArgument(name, raw string) error {
	api := s3err.New(s3err.CodeInvalidArgument, "Provided "+name+" is not an integer or within the allowed range")
	api.Extra = []s3err.Field{
		{Name: "ArgumentName", Value: name},
		{Name: "ArgumentValue", Value: raw},
	}
	return api
}

func (h *Handlers) owner() identity {
	return identity{ID: h.deps.Config.OwnerID, DisplayName: ownerDisplayName}
}

func initiatorOf(record UploadRecord, a sigv4.Auth) identity {
	initiator := identity{ID: record.InitiatorAccessKeyID, DisplayName: record.InitiatorAccessKeyID}
	if string(a.Key.ID) == record.InitiatorAccessKeyID && a.Key.Name != "" {
		initiator.DisplayName = a.Key.Name
	}
	return initiator
}

func (h *Handlers) listPartsResult(rt router.Route, a sigv4.Auth, record UploadRecord, page multipartstage.PartPage, marker, maxParts int) listPartsResult {
	result := listPartsResult{
		Bucket:            rt.Target.Bucket,
		Key:               rt.Target.Key,
		UploadID:          record.ID,
		PartNumberMarker:  marker,
		MaxParts:          maxParts,
		IsTruncated:       page.Truncated,
		Initiator:         initiatorOf(record, a),
		Owner:             h.owner(),
		StorageClass:      storageClassStandard,
		ChecksumAlgorithm: strings.ToUpper(record.ChecksumAlgorithm),
		ChecksumType:      record.ChecksumType,
		Parts:             make([]listedPart, 0, len(page.Parts)),
	}
	if page.Truncated {
		result.NextPartNumberMarker = page.NextMarker
	}
	for _, part := range page.Parts {
		listed := listedPart{
			PartNumber:   part.Number,
			LastModified: xmlcodec.FormatTime(part.StagedAt),
			ETag:         object.QuoteETag(part.ETag),
			Size:         part.Size,
		}
		listed.setChecksum(result.ChecksumAlgorithm, part.Checksum)
		result.Parts = append(result.Parts, listed)
	}
	return result
}

func (p *listedPart) setChecksum(algorithm, value string) {
	if value == "" {
		return
	}
	switch algorithm {
	case "CRC32":
		p.ChecksumCRC32 = value
	case "CRC32C":
		p.ChecksumCRC32C = value
	case "CRC64NVME":
		p.ChecksumCRC64NVME = value
	case "SHA1":
		p.ChecksumSHA1 = value
	case "SHA256":
		p.ChecksumSHA256 = value
	}
}

func (h *Handlers) ListMultipartUploads(w http.ResponseWriter, r *http.Request, rt router.Route, a sigv4.Auth) {
	info, err := h.authorize(r, rt, a, access.ActionList)
	if err != nil {
		h.fail(w, r, rt, err)
		return
	}
	request, err := parseUploadsRequest(r.URL.Query())
	if err != nil {
		h.fail(w, r, rt, err)
		return
	}
	page, err := h.deps.Stage.ListUploads(r.Context(), info.ChannelID, request.query)
	if err != nil {
		h.fail(w, r, rt, err)
		return
	}
	h.respond(w, r, rt, h.listUploadsResult(rt, request, page))
}

type uploadsRequest struct {
	query    multipartstage.UploadQuery
	encoding string
}

func parseUploadsRequest(values url.Values) (uploadsRequest, error) {
	maxUploads, err := parseListLimit(values, "max-uploads")
	if err != nil {
		return uploadsRequest{}, err
	}
	encoding := values.Get("encoding-type")
	if encoding != "" && encoding != urlEncoding {
		return uploadsRequest{}, s3err.New(s3err.CodeInvalidArgument, "Invalid Encoding Method specified in Request")
	}
	query := multipartstage.UploadQuery{
		Prefix:    values.Get("prefix"),
		Delimiter: values.Get("delimiter"),
		KeyMarker: values.Get("key-marker"),
		Max:       maxUploads,
	}
	if query.KeyMarker != "" {
		query.UploadIDMarker = values.Get("upload-id-marker")
	}
	return uploadsRequest{query: query, encoding: encoding}, nil
}

func (h *Handlers) listUploadsResult(rt router.Route, request uploadsRequest, page multipartstage.UploadPage) listUploadsResult {
	encode := keyEncoder(request.encoding)
	result := listUploadsResult{
		Bucket:         rt.Target.Bucket,
		KeyMarker:      encode(request.query.KeyMarker),
		UploadIDMarker: request.query.UploadIDMarker,
		Prefix:         encode(request.query.Prefix),
		Delimiter:      encode(request.query.Delimiter),
		MaxUploads:     request.query.Max,
		IsTruncated:    page.Truncated,
		EncodingType:   request.encoding,
		Uploads:        make([]listedUpload, 0, len(page.Uploads)),
	}
	if page.Truncated {
		result.NextKeyMarker = encode(page.NextKeyMarker)
		result.NextUploadIDMarker = page.NextUploadIDMarker
	}
	for _, upload := range page.Uploads {
		result.Uploads = append(result.Uploads, listedUpload{
			Key:          encode(string(upload.Key)),
			UploadID:     upload.ID,
			Initiator:    h.owner(),
			Owner:        h.owner(),
			StorageClass: storageClassStandard,
			Initiated:    xmlcodec.FormatTime(upload.InitiatedAt),
		})
	}
	for _, prefix := range page.CommonPrefixes {
		result.CommonPrefixes = append(result.CommonPrefixes, commonPrefix{Prefix: encode(prefix)})
	}
	return result
}

func keyEncoder(encoding string) func(string) string {
	if encoding != urlEncoding {
		return func(value string) string { return value }
	}
	return func(value string) string {
		escaped := strings.ReplaceAll(url.QueryEscape(value), "+", "%20")
		return strings.ReplaceAll(escaped, "%2F", "/")
	}
}
