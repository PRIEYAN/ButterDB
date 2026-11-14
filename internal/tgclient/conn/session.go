package conn

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/gotd/td/session"

	"github.com/PRIEYAN/ButterDB/internal/tgclient/scope"
)

type sessionStorage struct {
	store scope.SessionStore
}

var _ session.Storage = sessionStorage{}

func (s sessionStorage) LoadSession(ctx context.Context) ([]byte, error) {
	data, err := s.store.LoadSession(ctx)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, session.ErrNotFound) {
		return nil, session.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("conn: load session: %w", err)
	}
	if len(data) == 0 {
		return nil, session.ErrNotFound
	}
	return data, nil
}

func (s sessionStorage) StoreSession(ctx context.Context, data []byte) error {
	if err := s.store.StoreSession(ctx, data); err != nil {
		return fmt.Errorf("conn: store session: %w", err)
	}
	return nil
}
