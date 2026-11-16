package core

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"

	"github.com/PRIEYAN/ButterDB/internal/access/keys"
	"github.com/PRIEYAN/ButterDB/internal/adminapi/handlers"
	"github.com/PRIEYAN/ButterDB/internal/adminapi/session"
	"github.com/PRIEYAN/ButterDB/internal/adminapi/setup"
	"github.com/PRIEYAN/ButterDB/internal/adminapi/tlscert"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/lifecycle"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/platform/config"
	"github.com/PRIEYAN/ButterDB/internal/platform/datadir"
	"github.com/PRIEYAN/ButterDB/internal/storage/adminrepo"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/flow"
)

var _ lifecycle.Component = (*Admin)(nil)

type BuildInfo struct {
	Version string
	Commit  string
}

type AdminDependencies struct {
	Config   config.Config
	Data     *datadir.Dir
	Control  *sqlitedb.DB
	Keys     *keys.Service
	Relay    setup.LoginRelay
	Messages SavedMessagesSender
	Backend  *BackendSlot
	ServerID string
	Build    BuildInfo
	Files    tlscert.Filesystem
	Clock    clock.Clock
	Sleeper  flow.Sleeper
	Random   io.Reader
	Logger   *slog.Logger
	Hostname func() (string, error)
}

type Admin struct {
	deps    AdminDependencies
	repo    *adminrepo.Repository
	setup   *setup.Service
	handler http.Handler
	server  *adminServer
	tls     *adminTLS
	group   *lifecycle.Group
	certs   adminCertificateSource
	tokens  *session.AdminTokenHandler
}

func NewAdmin(deps AdminDependencies) (*Admin, error) {
	if err := validateAdminDependencies(deps); err != nil {
		return nil, err
	}
	if deps.Hostname == nil {
		deps.Hostname = os.Hostname
	}
	deps.Random = &lockedReader{source: deps.Random}
	a := &Admin{deps: deps, tls: newAdminTLS(deps.Config.Admin.TLSMode)}
	if err := a.assemble(); err != nil {
		return nil, err
	}
	return a, nil
}

type lockedReader struct {
	mu     sync.Mutex
	source io.Reader
}

func (l *lockedReader) Read(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.source.Read(p)
}

func (a *Admin) assemble() error {
	deps := a.deps
	var err error
	if a.repo, err = adminrepo.New(deps.Control, deps.Clock); err != nil {
		return fmt.Errorf("core: admin: repository: %w", err)
	}
	limiter := session.NewLimiter(session.DefaultLimiterConfig(), deps.Clock)
	tokens := session.NewTokens(deps.Random)
	claims := claimStore{repo: a.repo}
	relay := &limitedRelay{inner: deps.Relay, limiter: limiter, backend: deps.Backend}
	a.setup, err = setup.New(setup.Dependencies{
		Relay: relay, Claims: claims, Tokens: tokens, Clock: deps.Clock, Random: deps.Random, Logger: deps.Logger,
		TTL: deps.Config.Admin.SetupTokenTTL.Std(), ServerID: deps.ServerID,
	})
	if err != nil {
		return fmt.Errorf("core: admin: setup service: %w", err)
	}
	pairing := session.NewPairing(a.repo, savedMessagesDelivery{relay: deps.Relay, sender: deps.Messages}, tokens, deps.Clock, deps.Random)
	if a.tokens, err = session.NewAdminTokenHandler(session.AdminTokenDependencies{
		Claims: a.repo, Setup: a.setup, Pairing: pairing, Audit: a.repo, Clock: deps.Clock,
	}); err != nil {
		return fmt.Errorf("core: admin: admin token handler: %w", err)
	}
	if a.handler, err = a.assembleRouter(limiter, tokens, claims, pairing); err != nil {
		return err
	}
	a.certs = adminCertificateSource{
		cfg: deps.Config.Admin, serverID: deps.ServerID, dataDir: deps.Data.Path(), files: deps.Files,
		clock: deps.Clock, random: deps.Random, hostname: deps.Hostname,
	}
	a.server = newAdminServer(deps.Config.Admin, a.handler, a.tls, deps.Logger)
	return a.assembleGroup()
}

func (a *Admin) assembleRouter(limiter *session.Limiter, tokens *session.Tokens, claims claimStore, pairing *session.Pairing) (http.Handler, error) {
	deps := a.deps
	cfg := deps.Config
	trusted, err := parseTrustedProxies(cfg.Admin.TrustedProxies)
	if err != nil {
		return nil, err
	}
	gate := backendGate{slot: deps.Backend, relay: deps.Relay}
	audit := &auditLog{repo: a.repo}
	authenticator := session.NewAuthenticator(a.repo, limiter, deps.Clock)
	failures := newAuthFailureAuditor(a.repo, deps.Clock, deps.Logger)
	startedAt := deps.Clock.Now()
	status := newAdminStatus(adminStatusDependencies{
		config: cfg, relay: deps.Relay, slot: deps.Backend, clock: deps.Clock, serverID: deps.ServerID, build: deps.Build, startedAt: startedAt,
	})
	router, err := handlers.NewRouter(handlers.Dependencies{
		Buckets: adminBuckets{gate: gate},
		Keys:    deps.Keys,
		Lister:  adminObjectLister{gate: gate},
		Writer:  adminObjectWriter{gate: gate},
		Server:  newAdminObjectServer(gate, deps.Clock),
		Presign: &adminPresigner{secrets: deps.Keys, clock: deps.Clock, publicURL: cfg.S3.PublicURL, region: cfg.S3.Region},
		Status:  status,
		Audit:   audit,
		Setup: newSetupEndpoints(setupEndpointsDependencies{
			service: a.setup, claims: claims, relay: deps.Relay, limiter: limiter, clock: deps.Clock, logger: deps.Logger,
			serverID: deps.ServerID, build: deps.Build, tls: a.tls,
		}),
		Pairing: newPairingEndpoints(pairing, limiter, deps.ServerID),
		Devices: newDeviceService(a.repo, tokens, deps.Clock),
		Device:  session.DeviceFromContext,
		Auth:    failures.wrap(authenticator.Middleware),
		Clock:   deps.Clock,
		Random:  deps.Random,
		Logger:  deps.Logger,
		Config: handlers.Config{
			S3Endpoint:         s3Endpoint(cfg.S3),
			S3Region:           cfg.S3.Region,
			AllowedHosts:       adminAllowedHosts(cfg.Admin),
			TrustedProxies:     cfg.Admin.TrustedProxies,
			UploadCommitBudget: cfg.Admin.UploadCommitBudget.Std(),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("core: admin: router: %w", err)
	}
	return clientAddressHandler{trusted: trusted, next: router}, nil
}

func (a *Admin) assembleGroup() error {
	deps := a.deps
	sweeper, err := session.NewSweeper(session.SweeperDependencies{
		Store: a.repo, Audit: a.repo, Clock: deps.Clock, Logger: deps.Logger,
		Config: session.SweeperConfig{IdleRevoke: deps.Config.Admin.DeviceIdleRevoke.Std()},
	})
	if err != nil {
		return fmt.Errorf("core: admin: sweeper: %w", err)
	}
	a.group = lifecycle.NewGroup(deps.Config.Shutdown.ComponentTimeout.Std())
	a.group.Add("admin-certificate", lifecycleFunc{start: a.loadCertificate})
	a.group.Add("admin-setup-token", lifecycleFunc{start: a.mintSetupToken})
	a.group.Add("admin-sweep", newAdminSweep(sweeper, session.DefaultSweepInterval, deps.Sleeper, deps.Logger))
	a.group.Add("admin-control-socket", newControlSocket(deps.Config.Admin.ControlSocket, a.tokens, deps.Sleeper, deps.Logger))
	a.group.Add("admin-listener", a.server)
	return nil
}

type lifecycleFunc struct {
	start func(ctx context.Context) error
}

func (l lifecycleFunc) Start(ctx context.Context) error {
	return l.start(ctx)
}

func (lifecycleFunc) Close(context.Context) error {
	return nil
}

func (a *Admin) loadCertificate(context.Context) error {
	if a.deps.Config.Admin.TLSMode == adminTLSProxy {
		return nil
	}
	cert, spki, err := a.certs.load()
	if err != nil {
		return err
	}
	a.tls.install(cert, spki)
	return nil
}

func (a *Admin) mintSetupToken(ctx context.Context) error {
	if _, _, err := a.setup.MintSetupTokenIfUnclaimed(ctx); err != nil {
		return fmt.Errorf("core: admin: mint setup token: %w", err)
	}
	return nil
}

func (a *Admin) Start(ctx context.Context) error {
	if err := a.group.Start(ctx); err != nil {
		return fmt.Errorf("core: admin: start: %w", err)
	}
	return nil
}

func (a *Admin) Close(ctx context.Context) error {
	if err := a.group.Close(ctx); err != nil {
		return fmt.Errorf("core: admin: close: %w", err)
	}
	return nil
}

func (a *Admin) ReloadCertificate() error {
	if a.deps.Config.Admin.TLSMode != adminTLSFiles {
		return nil
	}
	cert, spki, err := a.certs.load()
	if err != nil {
		return err
	}
	a.tls.install(cert, spki)
	a.deps.Logger.Info("admin certificate reloaded", "component", "admin", "tls_fingerprint", tlscert.Fingerprint(spki))
	return nil
}

func (a *Admin) Handler() http.Handler {
	return a.handler
}

func (a *Admin) Addr() string {
	return a.server.addr()
}

func (a *Admin) Fingerprint() (string, bool) {
	spki, known := a.tls.fingerprint()
	if !known {
		return "", false
	}
	return tlscert.Fingerprint(spki), true
}

func s3Endpoint(cfg config.S3Config) string {
	if cfg.PublicURL != "" {
		return cfg.PublicURL
	}
	scheme := "http"
	if cfg.TLSMode == s3TLSModeFiles {
		scheme = "https"
	}
	return scheme + "://" + cfg.Listen
}

func validateAdminDependencies(deps AdminDependencies) error {
	missing := ""
	switch {
	case deps.Data == nil:
		missing = "data directory"
	case deps.Control == nil:
		missing = "control database"
	case deps.Keys == nil:
		missing = "access keys"
	case deps.Relay == nil:
		missing = "login relay"
	case deps.Messages == nil:
		missing = "saved messages sender"
	case deps.Backend == nil:
		missing = "backend slot"
	case deps.ServerID == "":
		missing = "server id"
	case deps.Files == nil:
		missing = "tls files"
	case deps.Clock == nil:
		missing = "clock"
	case deps.Sleeper == nil:
		missing = "sleeper"
	case deps.Random == nil:
		missing = "random"
	case deps.Logger == nil:
		missing = "logger"
	}
	if missing != "" {
		return fmt.Errorf("core: admin: %w: %s", errs.ErrMissingDependency, missing)
	}
	return nil
}
