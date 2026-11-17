package core

import (
	"context"
	cryptorand "crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/adminapi/session"
	"github.com/PRIEYAN/ButterDB/internal/buckets"
	"github.com/PRIEYAN/ButterDB/internal/crypto/keyring"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/storage/accountrepo"
	"github.com/PRIEYAN/ButterDB/internal/storage/adminrepo"
	"github.com/PRIEYAN/ButterDB/internal/storage/schema"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
	"github.com/PRIEYAN/ButterDB/internal/syncer/reconcile"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

const localOwnerDeviceID = "local-owner"

var ErrEscrowPassword = keyring.ErrEscrowPassword

func (a *Admin) authMiddleware(failures *authFailureAuditor, authenticator *session.Authenticator) func(http.Handler) http.Handler {
	if a.deps.Embedded {
		return localOwner
	}
	return failures.wrap(authenticator.Middleware)
}

func localOwner(next http.Handler) http.Handler {
	device := adminrepo.Device{DeviceID: localOwnerDeviceID, Name: "This computer", Platform: "local", CreatedAt: time.Unix(0, 0)}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := session.ContextWithDevice(session.ContextWithClientAddr(r.Context(), "127.0.0.1"), device)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func escrowFromOp(op wire.Op) keyring.Escrow {
	return keyring.Escrow{
		Params:      keyring.EscrowParams{Time: op.EscrowTime, MemoryKiB: op.EscrowMemoryKiB, Parallel: op.EscrowParallel},
		Salt:        op.EscrowSalt,
		Wrap:        op.WrappedKey,
		Fingerprint: op.KEKFingerprint,
	}
}

func EscrowOf(op wire.Op) keyring.Escrow {
	return escrowFromOp(op)
}

func publishEscrow(ctx context.Context, registry *buckets.RegistryProvisioner, keyFile string, password []byte, deploymentID string, clk clock.Clock, random io.Reader) (int64, error) {
	sealed, err := keyring.SealEscrowFile(keyFile, password, deploymentID, keyring.DefaultEscrowParams, random)
	if err != nil {
		return 0, fmt.Errorf("core: seal key escrow: %w", err)
	}
	msgID, err := registry.PublishEscrow(ctx, wire.Op{
		KEKFingerprint:  sealed.Fingerprint,
		EscrowKDF:       wire.EscrowKDFArgon2id,
		EscrowSalt:      sealed.Salt,
		EscrowTime:      sealed.Params.Time,
		EscrowMemoryKiB: sealed.Params.MemoryKiB,
		EscrowParallel:  sealed.Params.Parallel,
		WrappedKey:      sealed.Wrap,
	}, random)
	if err != nil {
		return 0, fmt.Errorf("core: publish key escrow: %w", err)
	}
	return msgID, nil
}

type projectHandle struct {
	registry  *buckets.RegistryProvisioner
	state     buckets.RegistryState
	channels  tgclient.ChannelRPC
	inspector *reconcile.Discoverer
}

func (s *Serving) project(ctx context.Context, logger *slog.Logger) (projectHandle, error) {
	rt := s.Runtime()
	if rt == nil || rt.Sync == nil || rt.Sync.Registry == nil {
		return projectHandle{}, ErrNotLinked
	}
	state, found, err := rt.Sync.Registry.State(ctx)
	if err != nil {
		return projectHandle{}, err
	}
	if !found {
		return projectHandle{}, ErrNotLinked
	}
	transport, err := s.session.StorageTransport(ctx)
	if err != nil {
		return projectHandle{}, err
	}
	inspector, err := reconcile.NewDiscoverer(reconcile.DiscoveryDependencies{
		Telegram: transport.Channels, Identity: reconcile.Identity{UserID: state.PrimaryUserID}, Logger: logger,
	})
	if err != nil {
		return projectHandle{}, err
	}
	return projectHandle{registry: rt.Sync.Registry, state: state, channels: transport.Channels, inspector: inspector}, nil
}

func (h projectHandle) peer() tgclient.InputPeer {
	return tgclient.InputPeer{ChannelID: h.state.ChannelID, AccessHash: h.state.AccessHash}
}

func (s *Serving) ChangeProjectPassword(ctx context.Context, a *App, oldPassword, newPassword []byte) error {
	h, err := s.project(ctx, a.logger)
	if err != nil {
		return err
	}
	before, err := h.inspector.Inspect(ctx, h.peer())
	if err != nil {
		return err
	}
	if before.HasEscrow() {
		if err := keyring.VerifyEscrow(escrowFromOp(before.Escrow), oldPassword, h.state.DeploymentID); err != nil {
			return err
		}
	}
	msgID, err := publishEscrow(ctx, h.registry, a.cfg.ServerKeyFile, newPassword, h.state.DeploymentID, a.clock, s.random())
	if err != nil {
		return err
	}
	after, err := h.inspector.Inspect(ctx, h.peer())
	if err != nil {
		return err
	}
	if after.EscrowMsgID != msgID {
		return errors.New("core: new key escrow is not the newest one in the registry")
	}
	if err := keyring.VerifyEscrow(escrowFromOp(after.Escrow), newPassword, h.state.DeploymentID); err != nil {
		return fmt.Errorf("core: new key escrow does not open: %w", err)
	}
	var stale []int64
	for _, id := range after.EscrowMsgIDs {
		if id < msgID {
			stale = append(stale, id)
		}
	}
	if len(stale) == 0 {
		return nil
	}
	if err := h.channels.DeleteMessages(ctx, h.peer(), stale); err != nil {
		a.logger.Warn("old key escrow messages were not deleted", "component", "escrow", "error", err)
	}
	return nil
}

func (s *Serving) PublishProjectEscrow(ctx context.Context, a *App, password []byte) error {
	h, err := s.project(ctx, a.logger)
	if err != nil {
		return err
	}
	_, err = publishEscrow(ctx, h.registry, a.cfg.ServerKeyFile, password, h.state.DeploymentID, a.clock, s.random())
	return err
}

type channelTitleEditor interface {
	EditChannelTitle(ctx context.Context, peer tgclient.InputPeer, title string) error
}

func (s *Serving) RenameProject(ctx context.Context, a *App, name string) error {
	title, err := buckets.ProjectTitle(name)
	if err != nil {
		return err
	}
	h, err := s.project(ctx, a.logger)
	if err != nil {
		return err
	}
	editor, ok := h.channels.(channelTitleEditor)
	if !ok {
		return errors.New("core: telegram client cannot rename channels")
	}
	return editor.EditChannelTitle(ctx, h.peer(), title)
}

func (s *Serving) AdminHandler() http.Handler {
	if s.Admin == nil {
		return nil
	}
	return s.Admin.Handler()
}

func (s *Serving) Session() *TelegramSession {
	return s.session
}

func (s *Serving) random() io.Reader {
	return cryptorand.Reader
}

type AccountRecord = accountrepo.Record

func (s *TelegramSession) PrimaryAccount(ctx context.Context) (AccountRecord, bool, error) {
	return s.accounts.Get(ctx, primaryRole)
}

func (a *App) SeedPrimaryAccount(ctx context.Context, rec AccountRecord) error {
	dir := a.DataDir()
	if dir == nil {
		return ErrAppNotStarted
	}
	control, err := sqlitedb.OpenWith(ctx, sqlitedb.Control, dir.Join(controlDatabaseFile), sqlitedb.Options{}, a.logger,
		sqlitedb.Dependencies{Clock: a.clock, FS: a.fsys})
	if err != nil {
		return fmt.Errorf("core: open control database: %w", err)
	}
	defer func() { _ = control.Close() }()
	if _, err := schema.Apply(ctx, control, schema.Control); err != nil {
		return fmt.Errorf("core: migrate control database: %w", err)
	}
	accounts, err := accountrepo.New(control)
	if err != nil {
		return err
	}
	existing, found, err := accounts.Get(ctx, primaryRole)
	if err != nil {
		return err
	}
	if found && existing.Linked && existing.UserID == rec.UserID {
		return nil
	}
	if found && existing.UserID != rec.UserID {
		return fmt.Errorf("core: this project belongs to another telegram account")
	}
	rec.Role = primaryRole
	rec.Linked = true
	return accounts.Link(ctx, rec)
}

func (s *Serving) RegistryReport() RegistryReport {
	if s.starter == nil {
		return RegistryReport{}
	}
	return s.starter.RegistryReport()
}
