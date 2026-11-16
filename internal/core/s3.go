package core

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/PRIEYAN/ButterDB/internal/access/keys"
	"github.com/PRIEYAN/ButterDB/internal/access/policy"
	"github.com/PRIEYAN/ButterDB/internal/access/sigv4"
	"github.com/PRIEYAN/ButterDB/internal/crypto/keyring"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/lifecycle"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/platform/config"
	"github.com/PRIEYAN/ButterDB/internal/readpath/httprange"
	"github.com/PRIEYAN/ButterDB/internal/s3api/bucketops"
	"github.com/PRIEYAN/ButterDB/internal/s3api/listing"
	"github.com/PRIEYAN/ButterDB/internal/s3api/multipart"
	"github.com/PRIEYAN/ButterDB/internal/s3api/objectops"
	"github.com/PRIEYAN/ButterDB/internal/s3api/router"
	"github.com/PRIEYAN/ButterDB/internal/storage/bucketrepo"
	"github.com/PRIEYAN/ButterDB/internal/storage/keyrepo"
	"github.com/PRIEYAN/ButterDB/internal/storage/objectrepo"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
)

var _ lifecycle.Component = (*S3)(nil)

type S3Dependencies struct {
	Config   config.Config
	Control  *sqlitedb.DB
	Database *sqlitedb.DB
	Ring     *keyring.Ring
	Write    *Write
	Read     *Read
	Clock    clock.Clock
	Random   io.Reader
	Logger   *slog.Logger
	Keys     *keys.Service
}

type S3 struct {
	Keys      *keys.Service
	Verifier  *sigv4.Verifier
	Policy    *policy.Evaluator
	Router    *router.Router
	Readiness *s3Readiness
	server    *s3Server
}

func NewS3(deps S3Dependencies) (*S3, error) {
	if err := validateS3Dependencies(deps); err != nil {
		return nil, err
	}
	s := &S3{Readiness: newS3Readiness()}
	if err := s.assembleAccess(deps); err != nil {
		return nil, err
	}
	handlers, err := s.assembleHandlers(deps)
	if err != nil {
		return nil, err
	}
	if err := s.assembleRouter(deps, handlers); err != nil {
		return nil, err
	}
	s.server = newS3Server(deps.Config.S3, s.Router, deps.Logger)
	return s, nil
}

func (s *S3) assembleAccess(deps S3Dependencies) error {
	var err error
	if s.Keys = deps.Keys; s.Keys == nil {
		if s.Keys, err = NewKeyService(deps.Control, deps.Ring, deps.Clock, deps.Random); err != nil {
			return err
		}
	}
	s.Verifier = sigv4.NewVerifier(sigv4.Config{Region: deps.Config.S3.Region, Now: deps.Clock.Now}, s.Keys)
	buckets, err := bucketrepo.New(deps.Database)
	if err != nil {
		return fmt.Errorf("core: s3: bucket repository: %w", err)
	}
	s.Policy = policy.NewEvaluator(buckets)
	return nil
}

func (s *S3) assembleHandlers(deps S3Dependencies) (map[router.Operation]router.HandlerFunc, error) {
	buckets, err := bucketrepo.New(deps.Database)
	if err != nil {
		return nil, fmt.Errorf("core: s3: bucket repository: %w", err)
	}
	objects, err := objectrepo.New(deps.Database)
	if err != nil {
		return nil, fmt.Errorf("core: s3: object repository: %w", err)
	}
	cfg := deps.Config.S3
	owner := deps.Ring.OwnerID()
	bucketHandlers, err := bucketops.NewHandlers(bucketops.Dependencies{
		Buckets:   s3BucketReader{buckets: buckets},
		Evaluator: s.Policy,
		Bodies:    s3BodyOpener{verifier: s.Verifier},
		Config:    bucketops.Config{Region: cfg.Region, OwnerID: owner, CORS: corsConfig(cfg)},
	})
	if err != nil {
		return nil, fmt.Errorf("core: s3: bucket handlers: %w", err)
	}
	objectHandlers, err := objectops.NewHandlers(objectops.Dependencies{
		Evaluator:  s.Policy,
		Payloads:   s.Verifier,
		Writer:     deps.Write.Put,
		Remover:    deps.Write.Remove,
		Opener:     deps.Read,
		Heads:      objects,
		Principals: s3PrincipalAttacher{},
		Streamer:   httprange.NewStreamer(),
		Clock:      deps.Clock,
		Logger:     deps.Logger,
		Config:     objectops.Config{BodyIdleTimeout: cfg.BodyIdleTimeout.Std(), KeepAliveInterval: cfg.KeepaliveInterval.Std()},
	})
	if err != nil {
		return nil, fmt.Errorf("core: s3: object handlers: %w", err)
	}
	listingHandlers, err := listing.NewHandlers(listing.Dependencies{
		Evaluator: s.Policy,
		Lister:    s3ObjectLister{objects: objects},
		Logger:    deps.Logger,
		Config:    listing.Config{Region: cfg.Region, OwnerID: owner},
	})
	if err != nil {
		return nil, fmt.Errorf("core: s3: listing handlers: %w", err)
	}
	multipartHandlers, err := multipart.NewHandlers(multipart.Dependencies{
		Stage:      deps.Write.Stage,
		Verifier:   s.Verifier,
		Clock:      deps.Clock,
		Authorizer: s.Policy,
		Uploads:    s3UploadLookup{index: deps.Write.Multipart},
		Sources:    deps.Read,
		Config: multipart.Config{
			KeepaliveInterval: cfg.KeepaliveInterval.Std(),
			BodyIdleTimeout:   cfg.BodyIdleTimeout.Std(),
			PublicURL:         cfg.PublicURL,
			OwnerID:           owner,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("core: s3: multipart handlers: %w", err)
	}
	return mergeRoutes(bucketHandlers.Routes(), objectHandlers.Routes(), listingHandlers.Routes(), multipartHandlers.Routes()), nil
}

func (s *S3) assembleRouter(deps S3Dependencies, handlers map[router.Operation]router.HandlerFunc) error {
	cfg := deps.Config.S3
	var err error
	s.Router, err = router.NewRouter(router.Dependencies{
		Addressing: router.Addressing{
			BaseDomains:  cfg.BaseDomains,
			AllowedHosts: publicHosts(cfg.PublicURL),
			LoopbackOnly: listensOnLoopback(cfg.Listen),
		},
		Authenticator: s.Verifier,
		Authorizer:    s.Policy,
		Readiness:     s.Readiness,
		CORS:          corsConfig(cfg),
		Handlers:      handlers,
		Random:        deps.Random,
		Logger:        deps.Logger,
	})
	if err != nil {
		return fmt.Errorf("core: s3: router: %w", err)
	}
	return nil
}

func (s *S3) Handler() http.Handler {
	return s.Router
}

func (s *S3) MarkReady() {
	s.Readiness.set(router.StateReady)
}

func (s *S3) MarkDegraded() {
	s.Readiness.set(router.StateDegraded)
}

func (s *S3) MarkStarting() {
	s.Readiness.set(router.StateStarting)
}

func (s *S3) Addr() string {
	return s.server.addr()
}

func (s *S3) Start(ctx context.Context) error {
	return s.server.Start(ctx)
}

func (s *S3) Close(ctx context.Context) error {
	return s.server.Close(ctx)
}

func mergeRoutes(sets ...map[router.Operation]router.HandlerFunc) map[router.Operation]router.HandlerFunc {
	merged := make(map[router.Operation]router.HandlerFunc)
	for _, routes := range sets {
		for op, handler := range routes {
			merged[op] = handler
		}
	}
	return merged
}

func corsConfig(cfg config.S3Config) router.CORSConfig {
	return router.CORSConfig{
		AllowedOrigins: cfg.CORSAllowedOrigins,
		AllowedMethods: cfg.CORSAllowedMethods,
		AllowedHeaders: cfg.CORSAllowedHeaders,
		ExposeHeaders:  cfg.CORSExposeHeaders,
		MaxAgeSeconds:  cfg.CORSMaxAge,
	}
}

func publicHosts(publicURL string) []string {
	if publicURL == "" {
		return nil
	}
	parsed, err := url.Parse(publicURL)
	if err != nil || parsed.Host == "" {
		return nil
	}
	return []string{parsed.Host}
}

func validateS3Dependencies(deps S3Dependencies) error {
	missing := ""
	switch {
	case deps.Control == nil:
		missing = "control database"
	case deps.Database == nil:
		missing = "projection database"
	case deps.Ring == nil:
		missing = "server key"
	case deps.Write == nil:
		missing = "write"
	case deps.Read == nil:
		missing = "read"
	case deps.Clock == nil:
		missing = "clock"
	case deps.Random == nil:
		missing = "random"
	case deps.Logger == nil:
		missing = "logger"
	}
	if missing != "" {
		return fmt.Errorf("core: s3: %w: %s", errs.ErrMissingDependency, missing)
	}
	return nil
}

func NewKeyService(control *sqlitedb.DB, ring *keyring.Ring, clk clock.Clock, random io.Reader) (*keys.Service, error) {
	repo, err := keyrepo.New(control)
	if err != nil {
		return nil, fmt.Errorf("core: key repository: %w", err)
	}
	service, err := keys.NewService(keys.Dependencies{Repo: repo, Sealer: ring, Clock: clk, Random: random})
	if err != nil {
		return nil, fmt.Errorf("core: key service: %w", err)
	}
	return service, nil
}
