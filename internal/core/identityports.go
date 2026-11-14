package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/PRIEYAN/ButterDB/internal/crypto/keyring"
	"github.com/PRIEYAN/ButterDB/internal/identity"
	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
	"github.com/PRIEYAN/ButterDB/internal/platform/secretfile"
	"github.com/PRIEYAN/ButterDB/internal/storage/accountrepo"
)

var (
	errSessionKeyUnavailable = errors.New("core: session key unavailable")

	_ identity.SessionKeys  = sessionKeys{}
	_ identity.AccountStore = accountStore{}
	_ identity.SecretWriter = (*secretfile.Writer)(nil)
	_ identity.FileReader   = osFileReader{}
	_ identity.FileRemover  = fsRemover{}
	_ identity.AuthRPC      = lazyAuth{}
	_ identity.SelfReader   = lazySelf{}
	_ AccountRecords        = (*accountrepo.Repository)(nil)
	_ SessionKeySource      = (*keyring.Ring)(nil)
)

type SessionKeySource interface {
	SessionKey() *keyring.Key
}

type AccountRecords interface {
	Get(ctx context.Context, role string) (accountrepo.Record, bool, error)
	FindByUser(ctx context.Context, userID int64) (string, bool, error)
	Link(ctx context.Context, rec accountrepo.Record) error
	Unlink(ctx context.Context, role string, at int64) error
}

type sessionKeys struct {
	source SessionKeySource
}

func (k sessionKeys) LeaseSessionKey() ([]byte, error) {
	key := k.source.SessionKey()
	if key == nil {
		return nil, errSessionKeyUnavailable
	}
	defer key.Clear()
	return bytes.Clone(key.Bytes()), nil
}

type accountStore struct {
	records AccountRecords
}

func (s accountStore) Get(ctx context.Context, role string) (identity.StoredAccount, bool, error) {
	rec, found, err := s.records.Get(ctx, role)
	if err != nil || !found {
		return identity.StoredAccount{}, found, err
	}
	return identity.StoredAccount(rec), true, nil
}

func (s accountStore) FindByUser(ctx context.Context, userID int64) (string, bool, error) {
	return s.records.FindByUser(ctx, userID)
}

func (s accountStore) Link(ctx context.Context, account identity.StoredAccount) error {
	return s.records.Link(ctx, accountrepo.Record(account))
}

func (s accountStore) Unlink(ctx context.Context, role string, at int64) error {
	return s.records.Unlink(ctx, role, at)
}

type osFileReader struct{}

// fsx has no read operation, so secret files are read straight from the operating system.
func (osFileReader) ReadFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("core: read %s: %w", path, err)
	}
	return data, nil
}

type fsRemover struct {
	fsys fsx.FS
}

func (r fsRemover) Remove(path string) error {
	return r.fsys.Remove(path)
}
