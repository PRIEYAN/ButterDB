package listing

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/access/sigv4"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/s3api/router"
	"github.com/PRIEYAN/ButterDB/internal/s3api/xmlcodec"
)

const (
	defaultMaxKeys       = 1000
	maxDelimiterBytes    = 1024
	storageClassStandard = "STANDARD"
	encodingTypeURL      = "url"
	invalidMaxKeysText   = "Provided max-keys not an integer or within integer range"
	invalidEncodingText  = "Invalid Encoding Method specified in Request"
	invalidDelimiterText = "The delimiter must be between 1 and 1024 bytes"
	tokenVersion         = 0x01
	tokenAfterPrefix     = 0x01
)

type listParams struct {
	prefix       string
	delimiter    string
	maxKeys      int
	encodingType string
}

func parseListParams(values url.Values) (listParams, error) {
	params := listParams{prefix: values.Get("prefix"), delimiter: values.Get("delimiter"), maxKeys: defaultMaxKeys}
	if len(params.delimiter) > maxDelimiterBytes {
		return listParams{}, invalidArgument(invalidDelimiterText)
	}
	switch encoding := values.Get("encoding-type"); encoding {
	case "":
	case encodingTypeURL:
		params.encodingType = encodingTypeURL
	default:
		return listParams{}, invalidArgument(invalidEncodingText)
	}
	raw, present := values["max-keys"]
	if !present {
		return params, nil
	}
	maxKeys, err := parseMaxKeys(raw[0])
	if err != nil {
		return listParams{}, err
	}
	params.maxKeys = maxKeys
	return params, nil
}

func parseMaxKeys(raw string) (int, error) {
	value, err := strconv.ParseInt(raw, 10, 64)
	switch {
	case errors.Is(err, strconv.ErrRange) && !strings.HasPrefix(raw, "-"):
		return defaultMaxKeys, nil
	case err != nil || value < 0:
		return 0, invalidArgument(invalidMaxKeysText)
	}
	return int(min(value, defaultMaxKeys)), nil
}

func (p listParams) encode(value string) string {
	if p.encodingType == encodingTypeURL {
		return url.QueryEscape(value)
	}
	return value
}

func (h *Handlers) ListObjectsV2(w http.ResponseWriter, r *http.Request, rt router.Route, a sigv4.Auth) {
	info, err := h.authorizeList(r, rt, a)
	if err != nil {
		h.fail(w, r, rt, a, err)
		return
	}
	values := r.URL.Query()
	params, err := parseListParams(values)
	if err != nil {
		h.fail(w, r, rt, a, err)
		return
	}
	token := values.Get("continuation-token")
	startAfter := values.Get("start-after")
	page, err := h.page(r, info, params, startAfter, token)
	if err != nil {
		h.fail(w, r, rt, a, err)
		return
	}
	result := h.resultV2(rt, params, page, token, startAfter, values.Get("fetch-owner") == "true")
	h.respond(w, r, rt, a, result)
}

func (h *Handlers) page(r *http.Request, info bucket.Info, params listParams, startAfter, token string) (object.ListPage, error) {
	if params.maxKeys == 0 {
		return object.ListPage{}, nil
	}
	query := object.ListQuery{
		Prefix:            params.prefix,
		Delimiter:         params.delimiter,
		StartAfter:        startAfter,
		ContinuationToken: token,
		Limit:             params.maxKeys,
	}
	if token != "" {
		query.StartAfter = ""
	}
	page, err := h.deps.Lister.List(r.Context(), info.ChannelID, query)
	if errors.Is(err, ErrInvalidContinuationToken) {
		return object.ListPage{}, invalidArgument(invalidTokenText)
	}
	return page, err
}

func (h *Handlers) resultV2(rt router.Route, params listParams, page object.ListPage, token, startAfter string, fetchOwner bool) listBucketResultV2 {
	result := listBucketResultV2{
		Name:                  rt.Target.Bucket,
		Prefix:                params.encode(params.prefix),
		Delimiter:             params.encode(params.delimiter),
		MaxKeys:               params.maxKeys,
		KeyCount:              len(page.Objects) + len(page.CommonPrefixes),
		IsTruncated:           page.NextToken != "",
		ContinuationToken:     token,
		NextContinuationToken: page.NextToken,
		StartAfter:            params.encode(startAfter),
		EncodingType:          params.encodingType,
		Contents:              h.contents(params, page.Objects, fetchOwner),
		CommonPrefixes:        commonPrefixes(params, page.CommonPrefixes),
	}
	return result
}

func (h *Handlers) contents(params listParams, objects []object.ObjectMeta, withOwner bool) []contentsEntry {
	entries := make([]contentsEntry, 0, len(objects))
	for _, meta := range objects {
		entry := contentsEntry{
			Key:          params.encode(meta.Key),
			LastModified: xmlcodec.FormatTime(time.Unix(meta.MsgDate, 0)),
			ETag:         object.QuoteETag(meta.ETag),
			Size:         meta.PlaintextSize,
			StorageClass: storageClassStandard,
		}
		if withOwner {
			entry.Owner = &ownerEntry{ID: h.deps.Config.OwnerID, DisplayName: ownerDisplayName}
		}
		entries = append(entries, entry)
	}
	return entries
}

func commonPrefixes(params listParams, prefixes []string) []prefixEntry {
	entries := make([]prefixEntry, 0, len(prefixes))
	for _, prefix := range prefixes {
		entries = append(entries, prefixEntry{Prefix: params.encode(prefix)})
	}
	return entries
}

func (h *Handlers) ListObjects(w http.ResponseWriter, r *http.Request, rt router.Route, a sigv4.Auth) {
	info, err := h.authorizeList(r, rt, a)
	if err != nil {
		h.fail(w, r, rt, a, err)
		return
	}
	values := r.URL.Query()
	params, err := parseListParams(values)
	if err != nil {
		h.fail(w, r, rt, a, err)
		return
	}
	marker := values.Get("marker")
	startAfter, token := markerQuery(params, marker)
	page, err := h.page(r, info, params, startAfter, token)
	if err != nil {
		h.fail(w, r, rt, a, err)
		return
	}
	h.respond(w, r, rt, a, h.resultV1(rt, params, page, marker))
}

func markerQuery(params listParams, marker string) (startAfter, token string) {
	if marker == "" {
		return "", ""
	}
	if isCommonPrefix(params, marker) {
		raw := append([]byte{tokenVersion, tokenAfterPrefix}, marker...)
		return "", base64.RawURLEncoding.EncodeToString(raw)
	}
	return marker, ""
}

func isCommonPrefix(params listParams, marker string) bool {
	if params.delimiter == "" || !strings.HasPrefix(marker, params.prefix) {
		return false
	}
	rest := marker[len(params.prefix):]
	return strings.Index(rest, params.delimiter) == len(rest)-len(params.delimiter)
}

func (h *Handlers) resultV1(rt router.Route, params listParams, page object.ListPage, marker string) listBucketResultV1 {
	truncated := page.NextToken != ""
	result := listBucketResultV1{
		Name:           rt.Target.Bucket,
		Prefix:         params.encode(params.prefix),
		Marker:         params.encode(marker),
		Delimiter:      params.encode(params.delimiter),
		MaxKeys:        params.maxKeys,
		IsTruncated:    truncated,
		EncodingType:   params.encodingType,
		Contents:       h.contents(params, page.Objects, true),
		CommonPrefixes: commonPrefixes(params, page.CommonPrefixes),
	}
	if truncated && params.delimiter != "" {
		result.NextMarker = params.encode(lastEntry(page))
	}
	return result
}

func lastEntry(page object.ListPage) string {
	last := ""
	if count := len(page.Objects); count > 0 {
		last = page.Objects[count-1].Key
	}
	if count := len(page.CommonPrefixes); count > 0 {
		last = max(last, page.CommonPrefixes[count-1])
	}
	return last
}
