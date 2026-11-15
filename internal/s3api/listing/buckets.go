package listing

import (
	"encoding/base64"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/access/policy"
	"github.com/PRIEYAN/ButterDB/internal/access/sigv4"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/s3api/router"
	"github.com/PRIEYAN/ButterDB/internal/s3api/xmlcodec"
)

const (
	maxBucketsLimit      = 10000
	invalidTokenText     = "The continuation token provided is incorrect"
	invalidMaxBucketText = "Provided max-buckets not an integer or within integer range"
)

type bucketQuery struct {
	prefix     string
	token      string
	max        int
	regionOnly bool
}

func (h *Handlers) ListBuckets(w http.ResponseWriter, r *http.Request, rt router.Route, a sigv4.Auth) {
	query, err := h.parseBucketQuery(r)
	if err != nil {
		h.fail(w, r, rt, a, err)
		return
	}
	visible, err := h.visibleBuckets(r, a)
	if err != nil {
		h.fail(w, r, rt, a, err)
		return
	}
	h.respond(w, r, rt, a, h.bucketResult(query, visible))
}

func (h *Handlers) visibleBuckets(r *http.Request, a sigv4.Auth) ([]bucket.Info, error) {
	visible, err := h.deps.Evaluator.VisibleBuckets(r.Context(), policy.Principal{Anonymous: a.Kind == sigv4.AuthAnonymous, Key: a.Key})
	if err != nil {
		return nil, err
	}
	sorted := slices.Clone(visible)
	slices.SortFunc(sorted, func(x, y bucket.Info) int { return strings.Compare(string(x.Name), string(y.Name)) })
	return sorted, nil
}

func (h *Handlers) parseBucketQuery(r *http.Request) (bucketQuery, error) {
	values := r.URL.Query()
	query := bucketQuery{prefix: values.Get("prefix"), max: maxBucketsLimit}
	if region, present := values["bucket-region"]; present && region[0] != h.deps.Config.Region {
		query.regionOnly = true
	}
	if raw, present := values["max-buckets"]; present {
		limit, err := strconv.Atoi(raw[0])
		if err != nil || limit < 1 || limit > maxBucketsLimit {
			return bucketQuery{}, invalidArgument(invalidMaxBucketText)
		}
		query.max = limit
	}
	if raw := values.Get("continuation-token"); raw != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil || len(decoded) == 0 {
			return bucketQuery{}, invalidArgument(invalidTokenText)
		}
		query.token = string(decoded)
	}
	return query, nil
}

func (h *Handlers) bucketResult(query bucketQuery, visible []bucket.Info) listAllMyBucketsResult {
	result := listAllMyBucketsResult{
		Owner:  ownerEntry{ID: h.deps.Config.OwnerID, DisplayName: ownerDisplayName},
		Prefix: query.prefix,
	}
	var matching []bucket.Info
	if !query.regionOnly {
		matching = filterBuckets(visible, query)
	}
	if len(matching) > query.max {
		matching = matching[:query.max]
		result.ContinuationToken = base64.RawURLEncoding.EncodeToString([]byte(matching[len(matching)-1].Name))
	}
	for _, info := range matching {
		result.Buckets.Buckets = append(result.Buckets.Buckets, bucketEntry{
			Name:         string(info.Name),
			CreationDate: xmlcodec.FormatTime(time.Unix(info.CreatedDate, 0)),
			BucketRegion: h.deps.Config.Region,
		})
	}
	return result
}

func filterBuckets(visible []bucket.Info, query bucketQuery) []bucket.Info {
	matching := make([]bucket.Info, 0, len(visible))
	for _, info := range visible {
		name := string(info.Name)
		if strings.HasPrefix(name, query.prefix) && name > query.token {
			matching = append(matching, info)
		}
	}
	return matching
}
