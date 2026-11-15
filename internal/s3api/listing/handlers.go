package listing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/PRIEYAN/ButterDB/internal/access/policy"
	"github.com/PRIEYAN/ButterDB/internal/access/sigv4"
	"github.com/PRIEYAN/ButterDB/internal/domain/access"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/s3api/router"
	"github.com/PRIEYAN/ButterDB/internal/s3api/s3err"
	"github.com/PRIEYAN/ButterDB/internal/s3api/xmlcodec"
)

const (
	defaultRegion    = "us-east-1"
	ownerDisplayName = "butterdb"
)

var ErrInvalidContinuationToken = errors.New("listing: invalid continuation token")

type PolicyEvaluator interface {
	Authorize(ctx context.Context, p policy.Principal, b bucket.BucketName, a access.Action) (bucket.Info, error)
	VisibleBuckets(ctx context.Context, p policy.Principal) ([]bucket.Info, error)
}

type ObjectLister interface {
	List(ctx context.Context, channelID int64, query object.ListQuery) (object.ListPage, error)
}

type Config struct {
	Region  string
	OwnerID string
}

type Dependencies struct {
	Evaluator PolicyEvaluator
	Lister    ObjectLister
	Logger    *slog.Logger
	Config    Config
}

type Handlers struct {
	deps Dependencies
}

func NewHandlers(deps Dependencies) (*Handlers, error) {
	switch {
	case deps.Evaluator == nil:
		return nil, fmt.Errorf("listing: %w: evaluator", errs.ErrMissingDependency)
	case deps.Lister == nil:
		return nil, fmt.Errorf("listing: %w: lister", errs.ErrMissingDependency)
	case deps.Logger == nil:
		return nil, fmt.Errorf("listing: %w: logger", errs.ErrMissingDependency)
	case deps.Config.OwnerID == "":
		return nil, fmt.Errorf("listing: %w: owner id", errs.ErrMissingDependency)
	}
	if deps.Config.Region == "" {
		deps.Config.Region = defaultRegion
	}
	return &Handlers{deps: deps}, nil
}

func (h *Handlers) Routes() map[router.Operation]router.HandlerFunc {
	return map[router.Operation]router.HandlerFunc{
		router.OpListBuckets:   h.ListBuckets,
		router.OpListObjectsV2: h.ListObjectsV2,
		router.OpListObjects:   h.ListObjects,
	}
}

func (h *Handlers) fail(w http.ResponseWriter, r *http.Request, rt router.Route, a sigv4.Auth, err error) {
	api := s3err.Map(err)
	h.deps.Logger.Info("s3 request failed",
		"request_id", w.Header().Get(s3err.HeaderRequestID),
		"operation", string(rt.Operation),
		"code", string(api.Code),
		"access_key_id", string(a.Key.ID),
	)
	router.WriteError(w, r, rt.Target, err)
}

func (h *Handlers) respond(w http.ResponseWriter, r *http.Request, rt router.Route, a sigv4.Auth, v any) {
	if err := xmlcodec.WriteResult(w, http.StatusOK, v); err != nil {
		h.fail(w, r, rt, a, err)
	}
}

func (h *Handlers) authorizeList(r *http.Request, rt router.Route, a sigv4.Auth) (bucket.Info, error) {
	if a.Kind == sigv4.AuthAnonymous {
		return bucket.Info{}, errs.ErrAccessDenied
	}
	return h.deps.Evaluator.Authorize(r.Context(), policy.Principal{Key: a.Key}, bucket.BucketName(rt.Target.Bucket), access.ActionList)
}

func invalidArgument(message string) error {
	return s3err.New(s3err.CodeInvalidArgument, message)
}
