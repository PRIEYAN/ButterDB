package conn

import (
	"context"
	"errors"
	"io/fs"
	"sync"
	"testing"

	"github.com/gotd/td/session"
)

type memoryStore struct {
	mu      sync.Mutex
	data    []byte
	loadErr error
}

func (m *memoryStore) LoadSession(context.Context) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.data, m.loadErr
}

func (m *memoryStore) StoreSession(_ context.Context, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data = append([]byte(nil), data...)
	return nil
}

func TestSessionStorageMissingSession(t *testing.T) {
	tests := []struct {
		name  string
		store *memoryStore
	}{
		{"empty bytes", &memoryStore{}},
		{"not exist error", &memoryStore{loadErr: fs.ErrNotExist}},
		{"gotd not found error", &memoryStore{loadErr: session.ErrNotFound}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := sessionStorage{store: tt.store}.LoadSession(context.Background())
			if !errors.Is(err, session.ErrNotFound) {
				t.Fatalf("LoadSession error = %v, want session.ErrNotFound", err)
			}
			if data != nil {
				t.Fatalf("LoadSession data = %q, want nil", data)
			}
		})
	}
}

func TestSessionStorageReportsStoreFailures(t *testing.T) {
	wantErr := errors.New("disk failure")
	_, err := sessionStorage{store: &memoryStore{loadErr: wantErr}}.LoadSession(context.Background())
	if !errors.Is(err, wantErr) || errors.Is(err, session.ErrNotFound) {
		t.Fatalf("LoadSession error = %v, want %v", err, wantErr)
	}
}

func TestSessionStorageReplacesSession(t *testing.T) {
	storage := sessionStorage{store: &memoryStore{}}
	ctx := context.Background()

	if err := storage.StoreSession(ctx, []byte("old session")); err != nil {
		t.Fatalf("store old session: %v", err)
	}
	if err := storage.StoreSession(ctx, []byte("new session")); err != nil {
		t.Fatalf("store new session: %v", err)
	}
	data, err := storage.LoadSession(ctx)
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if got, want := string(data), "new session"; got != want {
		t.Fatalf("session = %q, want %q", got, want)
	}
}
