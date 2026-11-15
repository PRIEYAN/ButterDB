package objectops

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/access/policy"
	"github.com/PRIEYAN/ButterDB/internal/access/sigv4"
	"github.com/PRIEYAN/ButterDB/internal/domain/access"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/readpath/httprange"
	"github.com/PRIEYAN/ButterDB/internal/readpath/objectreader"
	"github.com/PRIEYAN/ButterDB/internal/s3api/router"
	"github.com/PRIEYAN/ButterDB/internal/s3api/s3err"
)

const (
	defaultBodyIdleTimeout   = 60 * time.Second
	defaultKeepAliveInterval = 15 * time.Second
)

type PolicyEvaluator interface {
	Authorize(ctx context.Context, p policy.Principal, b bucket.BucketName, a access.Action) (bucket.Info, error)
	AuthorizeAnonymousRead(ctx context.Context, b bucket.BucketName) (bucket.Info, error)
}

type PayloadSource interface {
	Body(r *http.Request, a sigv4.Auth) (*sigv4.PayloadReader, error)
}

type ObjectWriter interface {
	Put(ctx context.Context, req object.PutRequest, body io.Reader) (object.ObjectMeta, error)
}

type ObjectRemover interface {
	Delete(ctx context.Context, name bucket.BucketName, key object.Key) error
}

type ObjectOpener interface {
	Open(ctx context.Context, bucketName, key string) (object.ObjectMeta, objectreader.Opened, error)
}

type ObjectHeads interface {
	Get(ctx context.Context, channelID int64, key string) (object.ObjectMeta, error)
}

type PrincipalAttacher interface {
	WithPrincipal(ctx context.Context, accessKeyID, requestID string) (context.Context, error)
}

type Config struct {
	BodyIdleTimeout   time.Duration
	KeepAliveInterval time.Duration
}

type Dependencies struct {
	Evaluator  PolicyEvaluator
	Payloads   PayloadSource
	Writer     ObjectWriter
	Remover    ObjectRemover
	Opener     ObjectOpener
	Heads      ObjectHeads
	Principals PrincipalAttacher
	Streamer   *httprange.Streamer
	Clock      clock.Clock
	Logger     *slog.Logger
	Config     Config
}

type Handlers struct {
	deps Dependencies
}

func NewHandlers(deps Dependencies) (*Handlers, error) {
	missing := ""
	switch {
	case deps.Evaluator == nil:
		missing = "evaluator"
	case deps.Payloads == nil:
		missing = "payloads"
	case deps.Writer == nil:
		missing = "writer"
	case deps.Remover == nil:
		missing = "remover"
	case deps.Opener == nil:
		missing = "opener"
	case deps.Heads == nil:
		missing = "heads"
	case deps.Principals == nil:
		missing = "principals"
	case deps.Streamer == nil:
		missing = "streamer"
	case deps.Clock == nil:
		missing = "clock"
	case deps.Logger == nil:
		missing = "logger"
	}
	if missing != "" {
		return nil, fmt.Errorf("objectops: %w: %s", errs.ErrMissingDependency, missing)
	}
	if deps.Config.BodyIdleTimeout == 0 {
		deps.Config.BodyIdleTimeout = defaultBodyIdleTimeout
	}
	if deps.Config.KeepAliveInterval <= 0 {
		deps.Config.KeepAliveInterval = defaultKeepAliveInterval
	}
	return &Handlers{deps: deps}, nil
}

func (h *Handlers) Routes() map[router.Operation]router.HandlerFunc {
	return map[router.Operation]router.HandlerFunc{
		router.OpPutObject:    h.PutObject,
		router.OpGetObject:    h.GetObject,
		router.OpHeadObject:   h.HeadObject,
		router.OpDeleteObject: h.DeleteObject,
	}
}

func (h *Handlers) authorize(r *http.Request, a sigv4.Auth, bucketName string, action access.Action) (bucket.Info, error) {
	if a.Kind == sigv4.AuthAnonymous {
		return bucket.Info{}, errs.ErrAccessDenied
	}
	return h.deps.Evaluator.Authorize(r.Context(), policy.Principal{Key: a.Key}, bucket.BucketName(bucketName), action)
}

func (h *Handlers) authorizeRead(r *http.Request, a sigv4.Auth, bucketName string) (bucket.Info, error) {
	if a.Kind == sigv4.AuthAnonymous {
		return h.deps.Evaluator.AuthorizeAnonymousRead(r.Context(), bucket.BucketName(bucketName))
	}
	return h.authorize(r, a, bucketName, access.ActionRead)
}

func (h *Handlers) fail(w http.ResponseWriter, r *http.Request, rt router.Route, a sigv4.Auth, err error) {
	h.logFailure(w, rt, a, err)
	router.WriteError(w, r, rt.Target, err)
}

func (h *Handlers) logFailure(w http.ResponseWriter, rt router.Route, a sigv4.Auth, err error) {
	api := s3err.Map(err)
	h.deps.Logger.Info("s3 request failed",
		"request_id", w.Header().Get(s3err.HeaderRequestID),
		"operation", string(rt.Operation),
		"code", string(api.Code),
		"access_key_id", string(a.Key.ID),
	)
}

func invalidArgument(message string) error {
	return s3err.New(s3err.CodeInvalidArgument, message)
}

func invalidRequest(message string) error {
	return s3err.New(s3err.CodeInvalidRequest, message)
}

func notImplemented(message string) error {
	return s3err.New(s3err.CodeNotImplemented, message)
}
