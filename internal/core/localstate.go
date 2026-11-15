package core

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/PRIEYAN/ButterDB/internal/crypto/keyring"
	"github.com/PRIEYAN/ButterDB/internal/platform/secretfile"
	"github.com/PRIEYAN/ButterDB/internal/storage/accountrepo"
	"github.com/PRIEYAN/ButterDB/internal/storage/schema"
	"github.com/PRIEYAN/ButterDB/internal/storage/sqlitedb"
)

var _ primaryAccountProbe = (*primaryAccounts)(nil)

type primaryAccounts struct {
	records AccountRecords
}

func (p *primaryAccounts) HasPrimaryAccount(ctx context.Context) (bool, error) {
	record, found, err := p.records.Get(ctx, primaryRole)
	if err != nil {
		return false, err
	}
	return found && record.Linked, nil
}

type unknownDeployment struct{}

func (unknownDeployment) DeploymentID(context.Context) (string, error) {
	return "", nil
}

func (a *App) VerifyLocalState(ctx context.Context) error {
	dir := a.DataDir()
	if dir == nil {
		return ErrAppNotStarted
	}
	statePath, controlPath := dir.Join(stateFileName), dir.Join(controlDatabaseFile)
	hasState, hasControl := fileExists(statePath), fileExists(controlPath)
	if !hasState && !hasControl {
		return nil
	}
	if !hasState {
		return a.checkUnanchored(ctx, controlPath)
	}
	writer, err := secretfile.New(a.fsys)
	if err != nil {
		return fmt.Errorf("core: local state: secret writer: %w", err)
	}
	state, err := newRegistryStateFile(statePath, writer, unknownDeployment{})
	if err != nil {
		return err
	}
	return a.checkAnchored(ctx, state, controlPath, hasControl)
}

func (a *App) checkUnanchored(ctx context.Context, controlPath string) error {
	accounts, closeControl, err := a.openAccounts(ctx, controlPath)
	if err != nil {
		return err
	}
	defer closeControl()
	return CheckUnanchored(ctx, accounts)
}

func (a *App) checkAnchored(ctx context.Context, state *registryStateFile, controlPath string, hasControl bool) error {
	if _, _, err := state.LoadRegistryState(ctx); err != nil {
		return err
	}
	ring, err := keyring.Load(a.cfg.ServerKeyFile)
	if err != nil {
		return fmt.Errorf("%w: server key: %w", ErrKeyOrRegistry, err)
	}
	defer ring.Close()
	var accounts primaryAccountProbe = primaryAlwaysLinked{}
	if hasControl {
		probe, closeControl, err := a.openAccounts(ctx, controlPath)
		if err != nil {
			return err
		}
		defer closeControl()
		accounts = probe
	}
	projectionDB, err := a.openProjectionDatabase(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = projectionDB.Close() }()
	checker, err := NewLocalStateChecker(LocalStateDependencies{
		State:       state,
		Fingerprint: ring.FingerprintString(),
		Database:    projectionDB,
		Accounts:    accounts,
	})
	if err != nil {
		return err
	}
	return checker.Check(ctx)
}

func (a *App) openAccounts(ctx context.Context, controlPath string) (*primaryAccounts, func(), error) {
	control, err := sqlitedb.OpenWith(ctx, sqlitedb.Control, controlPath, sqlitedb.Options{}, a.logger,
		sqlitedb.Dependencies{Clock: a.clock, FS: a.fsys})
	if err != nil {
		return nil, nil, fmt.Errorf("core: open control database: %w", err)
	}
	closeControl := func() { _ = control.Close() }
	if _, err := schema.Apply(ctx, control, schema.Control); err != nil {
		closeControl()
		if errors.Is(err, schema.ErrSchemaTooNew) {
			return nil, nil, fmt.Errorf("%w: %w", ErrIncompatibleData, err)
		}
		return nil, nil, fmt.Errorf("core: migrate control database: %w", err)
	}
	records, err := accountrepo.New(control)
	if err != nil {
		closeControl()
		return nil, nil, fmt.Errorf("core: account repository: %w", err)
	}
	return &primaryAccounts{records: records}, closeControl, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return !errors.Is(err, fs.ErrNotExist)
}
