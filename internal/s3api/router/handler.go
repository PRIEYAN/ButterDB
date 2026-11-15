package router

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
	"github.com/PRIEYAN/ButterDB/internal/s3api/s3err"
)

const (
	startingRetryAfter = 30 * time.Second
	degradedRetryAfter = 5 * time.Second
)

type HandlerFunc func(w http.ResponseWriter, r *http.Request, rt Route, a sigv4.Auth)

type ServerState int

const (
	StateReady ServerState = iota
	StateStarting
	StateDegraded
)

type Authenticator interface {
	Authenticate(ctx context.Context, r *http.Request) (sigv4.Auth, error)
}

type Authorizer interface {
	Authorize(ctx context.Context, p policy.Principal, b bucket.BucketName, a access.Action) (bucket.Info, error)
	AuthorizeAnonymousRead(ctx context.Context, b bucket.BucketName) (bucket.Info, error)
}

type Readiness interface {
	State() ServerState
}

type Dependencies struct {
	Addressing    Addressing
	Authenticator Authenticator
	Authorizer    Authorizer
	Readiness     Readiness
	CORS          CORSConfig
	Handlers      map[Operation]HandlerFunc
	Random        io.Reader
	Logger        *slog.Logger
}

type Router struct {
	deps     Dependencies
	handlers map[Operation]HandlerFunc
	actions  map[Operation]access.Action
}

func NewRouter(deps Dependencies) (*Router, error) {
	switch {
	case deps.Authenticator == nil:
		return nil, fmt.Errorf("router: %w: authenticator", errs.ErrMissingDependency)
	case deps.Authorizer == nil:
		return nil, fmt.Errorf("router: %w: authorizer", errs.ErrMissingDependency)
	case deps.Readiness == nil:
		return nil, fmt.Errorf("router: %w: readiness", errs.ErrMissingDependency)
	case deps.Random == nil:
		return nil, fmt.Errorf("router: %w: random", errs.ErrMissingDependency)
	case deps.Logger == nil:
		return nil, fmt.Errorf("router: %w: logger", errs.ErrMissingDependency)
	case deps.Handlers == nil:
		return nil, fmt.Errorf("router: %w: handlers", errs.ErrMissingDependency)
	}
	handlers := make(map[Operation]HandlerFunc, len(deps.Handlers))
	for op, handler := range deps.Handlers {
		if handler == nil {
			return nil, fmt.Errorf("router: %w: handler for %s", errs.ErrMissingDependency, op)
		}
		handlers[op] = handler
	}
	return &Router{deps: deps, handlers: handlers, actions: Actions()}, nil
}

func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rt.prepareHeaders(w, r)
	target, err := ParseTarget(r, rt.deps.Addressing)
	if err != nil {
		rt.fail(w, r, Target{}, "", "", err)
		return
	}
	if r.Method == http.MethodOptions {
		rt.deps.CORS.preflight(w, r)
		return
	}
	if rejected := rt.checkReadiness(w, r, target); rejected {
		return
	}
	route, err := Resolve(r, target)
	if err != nil {
		rt.fail(w, r, target, "", "", err)
		return
	}
	rt.authenticateAndDispatch(w, r, route)
}

func (rt *Router) prepareHeaders(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Server", "butterdb")
	requestID, hostID, err := s3err.NewIDs(rt.deps.Random)
	if err != nil {
		rt.deps.Logger.Error("s3 request ids unavailable", "error", err)
	} else {
		h.Set(s3err.HeaderRequestID, requestID)
		h.Set(s3err.HeaderHostID, hostID)
	}
	if r.Method != http.MethodOptions {
		rt.deps.CORS.applyActual(h, r)
	}
}

func (rt *Router) checkReadiness(w http.ResponseWriter, r *http.Request, target Target) bool {
	switch rt.deps.Readiness.State() {
	case StateStarting:
		api := s3err.New(s3err.CodeServiceUnavailable, "")
		api.RetryAfter = startingRetryAfter
		rt.fail(w, r, target, "", "", api)
		return true
	case StateDegraded:
		if isWriteMethod(r.Method) {
			api := s3err.New(s3err.CodeSlowDown, "")
			api.RetryAfter = degradedRetryAfter
			rt.fail(w, r, target, "", "", api)
			return true
		}
	}
	return false
}

func isWriteMethod(method string) bool {
	return method == http.MethodPut || method == http.MethodPost || method == http.MethodDelete
}

func (rt *Router) authenticateAndDispatch(w http.ResponseWriter, r *http.Request, route Route) {
	auth, err := rt.deps.Authenticator.Authenticate(r.Context(), r)
	if err != nil {
		rt.fail(w, r, route.Target, route.Operation, "", err)
		return
	}
	if err := rt.authorize(r.Context(), auth, route); err != nil {
		rt.fail(w, r, route.Target, route.Operation, string(auth.Key.ID), err)
		return
	}
	handler, ok := rt.handlers[route.Operation]
	if !ok {
		err := fmt.Errorf("router: %w: %s", errs.ErrNotImplemented, route.Operation)
		rt.fail(w, r, route.Target, route.Operation, string(auth.Key.ID), err)
		return
	}
	handler(w, r, route, auth)
}

func (rt *Router) authorize(ctx context.Context, auth sigv4.Auth, route Route) error {
	if auth.Kind == sigv4.AuthAnonymous {
		return rt.authorizeAnonymous(ctx, route)
	}
	action, scoped := rt.actions[route.Operation]
	if !scoped {
		return nil
	}
	principal := policy.Principal{Key: auth.Key}
	_, err := rt.deps.Authorizer.Authorize(ctx, principal, bucket.BucketName(route.Target.Bucket), action)
	return err
}

func (rt *Router) authorizeAnonymous(ctx context.Context, route Route) error {
	if route.Operation != OpGetObject && route.Operation != OpHeadObject {
		return errs.ErrAccessDenied
	}
	_, err := rt.deps.Authorizer.AuthorizeAnonymousRead(ctx, bucket.BucketName(route.Target.Bucket))
	return err
}

func (rt *Router) fail(w http.ResponseWriter, r *http.Request, target Target, op Operation, accessKeyID string, err error) {
	api := describeTarget(s3err.Map(err), target)
	rt.deps.Logger.Info("s3 request failed",
		"request_id", w.Header().Get(s3err.HeaderRequestID),
		"operation", string(op),
		"code", string(api.Code),
		"access_key_id", accessKeyID,
	)
	s3err.Write(w, r, api)
}

func WriteError(w http.ResponseWriter, r *http.Request, target Target, err error) {
	s3err.Write(w, r, describeTarget(s3err.Map(err), target))
}

func describeTarget(api s3err.APIError, target Target) s3err.APIError {
	switch api.Code {
	case s3err.CodeNoSuchBucket:
		api.BucketName = target.Bucket
	case s3err.CodeNoSuchKey, s3err.CodeNoSuchUpload:
		api.BucketName = target.Bucket
		api.Key = target.Key
	}
	if api.Resource == "" && target.Bucket != "" {
		api.Resource = "/" + target.Bucket
		if target.HasKey {
			api.Resource += "/" + target.Key
		}
	}
	return api
}
