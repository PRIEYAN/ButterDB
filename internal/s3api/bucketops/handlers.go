package bucketops

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/PRIEYAN/ButterDB/internal/access/policy"
	"github.com/PRIEYAN/ButterDB/internal/access/sigv4"
	"github.com/PRIEYAN/ButterDB/internal/domain/access"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/s3api/router"
	"github.com/PRIEYAN/ButterDB/internal/s3api/s3err"
	"github.com/PRIEYAN/ButterDB/internal/s3api/xmlcodec"
)

const (
	ownerDisplayName     = "butterdb"
	defaultRegion        = "us-east-1"
	aclBodyLimit         = 64 << 10
	creationDeniedText   = "Bucket creation is available only through the butterDB admin API"
	deletionDeniedText   = "Bucket deletion is available only through the butterDB admin API"
	bucketRegionHeader   = "X-Amz-Bucket-Region"
	canonicalUserGrantee = "CanonicalUser"
	groupGrantee         = "Group"
	allUsersURI          = "http://acs.amazonaws.com/groups/global/AllUsers"
	fullControl          = "FULL_CONTROL"
)

type BucketReader interface {
	Get(ctx context.Context, name bucket.BucketName) (bucket.Info, error)
}

type PolicyEvaluator interface {
	Authorize(ctx context.Context, p policy.Principal, b bucket.BucketName, a access.Action) (bucket.Info, error)
}

type BodyOpener interface {
	OpenBody(r *http.Request, a sigv4.Auth) (io.ReadCloser, error)
}

type Config struct {
	Region  string
	OwnerID string
	CORS    router.CORSConfig
}

type Dependencies struct {
	Buckets   BucketReader
	Evaluator PolicyEvaluator
	Bodies    BodyOpener
	Config    Config
}

type Handlers struct {
	deps Dependencies
}

func NewHandlers(deps Dependencies) (*Handlers, error) {
	switch {
	case deps.Buckets == nil:
		return nil, fmt.Errorf("bucketops: %w: buckets", errs.ErrMissingDependency)
	case deps.Evaluator == nil:
		return nil, fmt.Errorf("bucketops: %w: evaluator", errs.ErrMissingDependency)
	case deps.Bodies == nil:
		return nil, fmt.Errorf("bucketops: %w: bodies", errs.ErrMissingDependency)
	case deps.Config.OwnerID == "":
		return nil, fmt.Errorf("bucketops: %w: owner id", errs.ErrMissingDependency)
	}
	if deps.Config.Region == "" {
		deps.Config.Region = defaultRegion
	}
	return &Handlers{deps: deps}, nil
}

func (h *Handlers) Routes() map[router.Operation]router.HandlerFunc {
	return map[router.Operation]router.HandlerFunc{
		router.OpHeadBucket:            h.HeadBucket,
		router.OpGetBucketLocation:     h.GetBucketLocation,
		router.OpCreateBucket:          h.CreateBucket,
		router.OpDeleteBucket:          h.DeleteBucket,
		router.OpGetBucketPolicy:       h.GetBucketPolicy,
		router.OpGetBucketPolicyStatus: h.GetBucketPolicyStatus,
		router.OpGetBucketCors:         h.GetBucketCors,
		router.OpGetBucketAcl:          h.GetBucketAcl,
		router.OpPutBucketAcl:          h.PutBucketAcl,
		router.OpGetBucketVersioning:   h.GetBucketVersioning,
		router.OpGetBucketEncryption:   h.GetBucketEncryption,
		router.OpGetBucketConfig:       h.GetBucketConfig,
	}
}

func (h *Handlers) authorize(r *http.Request, rt router.Route, a sigv4.Auth) (bucket.Info, error) {
	action, scoped := router.ActionFor(rt.Operation)
	if a.Kind == sigv4.AuthAnonymous || !scoped {
		return bucket.Info{}, errs.ErrAccessDenied
	}
	principal := policy.Principal{Key: a.Key}
	return h.deps.Evaluator.Authorize(r.Context(), principal, bucket.BucketName(rt.Target.Bucket), action)
}

func (h *Handlers) fail(w http.ResponseWriter, r *http.Request, rt router.Route, err error) {
	router.WriteError(w, r, rt.Target, err)
}

func (h *Handlers) failWith(w http.ResponseWriter, r *http.Request, rt router.Route, code s3err.Code, message string) {
	api := s3err.New(code, message)
	api.BucketName = rt.Target.Bucket
	router.WriteError(w, r, rt.Target, api)
}

func (h *Handlers) respond(w http.ResponseWriter, r *http.Request, rt router.Route, v any) {
	if err := xmlcodec.WriteResult(w, http.StatusOK, v); err != nil {
		h.fail(w, r, rt, err)
	}
}

func (h *Handlers) HeadBucket(w http.ResponseWriter, r *http.Request, rt router.Route, a sigv4.Auth) {
	if _, err := h.authorize(r, rt, a); err != nil {
		h.fail(w, r, rt, err)
		return
	}
	w.Header().Set(bucketRegionHeader, h.deps.Config.Region)
	w.WriteHeader(http.StatusOK)
}

func (h *Handlers) GetBucketLocation(w http.ResponseWriter, r *http.Request, rt router.Route, a sigv4.Auth) {
	if _, err := h.authorize(r, rt, a); err != nil {
		h.fail(w, r, rt, err)
		return
	}
	constraint := h.deps.Config.Region
	if constraint == defaultRegion {
		constraint = ""
	}
	h.respond(w, r, rt, locationConstraint{Value: constraint})
}

func (h *Handlers) CreateBucket(w http.ResponseWriter, r *http.Request, rt router.Route, a sigv4.Auth) {
	if a.Kind == sigv4.AuthAnonymous {
		h.fail(w, r, rt, errs.ErrAccessDenied)
		return
	}
	if h.ownsInScopeBucket(r.Context(), rt, a) {
		h.failWith(w, r, rt, s3err.CodeBucketAlreadyOwnedByYou, "")
		return
	}
	h.failWith(w, r, rt, s3err.CodeAccessDenied, creationDeniedText)
}

func (h *Handlers) ownsInScopeBucket(ctx context.Context, rt router.Route, a sigv4.Auth) bool {
	if !a.Key.Scope.CoversBucket(rt.Target.Bucket) {
		return false
	}
	_, err := h.deps.Buckets.Get(ctx, bucket.BucketName(rt.Target.Bucket))
	return err == nil
}

func (h *Handlers) DeleteBucket(w http.ResponseWriter, r *http.Request, rt router.Route, _ sigv4.Auth) {
	h.failWith(w, r, rt, s3err.CodeAccessDenied, deletionDeniedText)
}
