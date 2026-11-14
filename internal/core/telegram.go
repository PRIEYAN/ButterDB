package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/identity"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/platform/config"
	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
	"github.com/PRIEYAN/ButterDB/internal/platform/secretfile"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/flow"
)

var errTelegramClosed = errors.New("core: telegram closed")

type TelegramDependencies struct {
	Config   config.Config
	Role     identity.Role
	Clock    clock.Clock
	FS       fsx.FS
	Random   io.Reader
	Sleeper  flow.Sleeper
	Logger   *slog.Logger
	Keys     SessionKeySource
	Accounts AccountRecords
}

type Telegram struct {
	deps     TelegramDependencies
	identity *identity.Service
	sessions *identity.SessionStore
	flow     *FlowControl

	mu        sync.Mutex
	transport *Transport
	closed    bool
}

func NewTelegram(deps TelegramDependencies) (*Telegram, error) {
	if err := validateTelegramDependencies(deps); err != nil {
		return nil, err
	}
	writer, err := secretfile.New(deps.FS)
	if err != nil {
		return nil, fmt.Errorf("core: telegram: secret writer: %w", err)
	}
	sessions, err := identity.NewSessionStore(identity.SessionStoreDependencies{
		Role:    deps.Role,
		Path:    sessionPath(deps.Config.Telegram, deps.Role),
		Keys:    sessionKeys{source: deps.Keys},
		Writer:  writer,
		Reader:  osFileReader{},
		Remover: fsRemover{fsys: deps.FS},
		Random:  deps.Random,
	})
	if err != nil {
		return nil, fmt.Errorf("core: telegram: session store: %w", err)
	}
	flowControl, err := NewFlowControl(FlowDependencies{
		SendRate: deps.Config.Telegram.SendRate,
		Clock:    deps.Clock,
		Sleeper:  deps.Sleeper,
		Random:   deps.Random,
		Logger:   deps.Logger,
	})
	if err != nil {
		return nil, err
	}
	t := &Telegram{deps: deps, sessions: sessions, flow: flowControl}
	t.identity, err = identity.NewService(identity.Dependencies{
		Role:            deps.Role,
		CredentialsPath: deps.Config.Telegram.AppFile,
		Writer:          writer,
		Reader:          osFileReader{},
		Remover:         fsRemover{fsys: deps.FS},
		Auth:            lazyAuth{telegram: t},
		Self:            lazySelf{telegram: t},
		Accounts:        accountStore{records: deps.Accounts},
		Sessions:        sessions,
		Clock:           deps.Clock,
		Random:          deps.Random,
		Logger:          deps.Logger,
	})
	if err != nil {
		return nil, fmt.Errorf("core: telegram: identity: %w", err)
	}
	return t, nil
}

func validateTelegramDependencies(deps TelegramDependencies) error {
	switch {
	case deps.Clock == nil:
		return fmt.Errorf("core: telegram: %w: clock", errs.ErrMissingDependency)
	case deps.FS == nil:
		return fmt.Errorf("core: telegram: %w: filesystem", errs.ErrMissingDependency)
	case deps.Random == nil:
		return fmt.Errorf("core: telegram: %w: random", errs.ErrMissingDependency)
	case deps.Sleeper == nil:
		return fmt.Errorf("core: telegram: %w: sleeper", errs.ErrMissingDependency)
	case deps.Logger == nil:
		return fmt.Errorf("core: telegram: %w: logger", errs.ErrMissingDependency)
	case deps.Keys == nil:
		return fmt.Errorf("core: telegram: %w: session keys", errs.ErrMissingDependency)
	case deps.Accounts == nil:
		return fmt.Errorf("core: telegram: %w: accounts", errs.ErrMissingDependency)
	}
	return nil
}

func sessionPath(cfg config.TelegramConfig, role identity.Role) string {
	if role == identity.RoleReplica {
		return cfg.ReplicaSessionFile
	}
	return cfg.SessionFile
}

func (t *Telegram) Identity() *identity.Service {
	return t.identity
}

func (t *Telegram) Flow() *FlowControl {
	return t.flow
}

func (t *Telegram) Transport(ctx context.Context) (*Transport, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil, errTelegramClosed
	}
	if t.transport != nil {
		return t.transport, nil
	}
	credentials, err := t.identity.LoadCredentials(ctx)
	if err != nil {
		return nil, fmt.Errorf("core: telegram: %w", err)
	}
	transport, err := NewTransport(TransportDependencies{
		Credentials: credentials,
		Sessions:    t.sessions,
		Flow:        t.flow,
		Telegram:    t.deps.Config.Telegram,
		Clock:       t.deps.Clock,
		Random:      t.deps.Random,
		Logger:      t.deps.Logger,
	})
	if err != nil {
		return nil, err
	}
	if err := transport.Start(ctx); err != nil {
		_ = transport.Close(ctx)
		return nil, fmt.Errorf("core: telegram: %w", err)
	}
	t.transport = transport
	return transport, nil
}

func (t *Telegram) Close(ctx context.Context) error {
	t.mu.Lock()
	t.closed = true
	transport := t.transport
	t.transport = nil
	t.mu.Unlock()
	if transport == nil {
		return nil
	}
	return transport.Close(ctx)
}

type lazyAuth struct {
	telegram *Telegram
}

func (l lazyAuth) SendCode(ctx context.Context, phone string) (tgclient.SentCode, error) {
	transport, err := l.telegram.Transport(ctx)
	if err != nil {
		return tgclient.SentCode{}, err
	}
	return transport.Auth.SendCode(ctx, phone)
}

func (l lazyAuth) SignIn(ctx context.Context, phone, phoneCodeHash, code string) (tgclient.Account, error) {
	transport, err := l.telegram.Transport(ctx)
	if err != nil {
		return tgclient.Account{}, err
	}
	return transport.Auth.SignIn(ctx, phone, phoneCodeHash, code)
}

func (l lazyAuth) CheckPassword(ctx context.Context, password string) (tgclient.Account, error) {
	transport, err := l.telegram.Transport(ctx)
	if err != nil {
		return tgclient.Account{}, err
	}
	return transport.Auth.CheckPassword(ctx, password)
}

func (l lazyAuth) GetPasswordHint(ctx context.Context) (string, error) {
	transport, err := l.telegram.Transport(ctx)
	if err != nil {
		return "", err
	}
	return transport.Auth.GetPasswordHint(ctx)
}

func (l lazyAuth) LogOut(ctx context.Context) error {
	transport, err := l.telegram.Transport(ctx)
	if err != nil {
		return err
	}
	return transport.Auth.LogOut(ctx)
}

type lazySelf struct {
	telegram *Telegram
}

func (l lazySelf) Self(ctx context.Context) (tgclient.Account, error) {
	transport, err := l.telegram.Transport(ctx)
	if err != nil {
		return tgclient.Account{}, err
	}
	return transport.Channels.Self(ctx)
}
