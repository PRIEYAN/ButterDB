package core

import (
	"context"
	"database/sql"
	"encoding/base32"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
)

const (
	serverIDKey      = "server_id"
	serverIDRawBytes = 16
)

var _ deploymentSource = (*adminMeta)(nil)

type adminMetaDatabase interface {
	Read(ctx context.Context, fn func(tx *sql.Tx) error) error
	Write(ctx context.Context, fn func(tx *sql.Tx) error) error
}

type adminMeta struct {
	db     adminMetaDatabase
	random io.Reader
}

func newAdminMeta(db adminMetaDatabase, random io.Reader) (*adminMeta, error) {
	if db == nil {
		return nil, fmt.Errorf("core: admin meta: %w: database", errs.ErrMissingDependency)
	}
	if random == nil {
		return nil, fmt.Errorf("core: admin meta: %w: random", errs.ErrMissingDependency)
	}
	return &adminMeta{db: db, random: random}, nil
}

func (m *adminMeta) DeploymentID(ctx context.Context) (string, error) {
	var value string
	err := m.db.Read(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `SELECT value FROM admin_meta WHERE key = ?1`, serverIDKey).Scan(&value)
		if errors.Is(err, sql.ErrNoRows) {
			value = ""
			return nil
		}
		return err
	})
	if err != nil {
		return "", fmt.Errorf("core: read server id: %w", err)
	}
	return value, nil
}

func (m *adminMeta) EnsureDeploymentID(ctx context.Context) (string, error) {
	existing, err := m.DeploymentID(ctx)
	if err != nil || existing != "" {
		return existing, err
	}
	var raw [serverIDRawBytes]byte
	if _, err := io.ReadFull(m.random, raw[:]); err != nil {
		return "", fmt.Errorf("core: mint server id: %w", err)
	}
	minted := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw[:]))
	if err := m.SetDeploymentID(ctx, minted); err != nil {
		return "", err
	}
	return minted, nil
}

func (m *adminMeta) SetDeploymentID(ctx context.Context, deploymentID string) error {
	err := m.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO admin_meta (key, value) VALUES (?1, ?2) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
			serverIDKey, deploymentID)
		return err
	})
	if err != nil {
		return fmt.Errorf("core: write server id: %w", err)
	}
	return nil
}
