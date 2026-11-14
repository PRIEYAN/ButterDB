package identity

import (
	"context"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sync"

	"golang.org/x/crypto/chacha20poly1305"

	"github.com/PRIEYAN/ButterDB/internal/crypto/keyring"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
)

const (
	sealedSessionVersion = 1
	sessionAADPrefix     = "butterdb/session/v1|"
)

type SessionKeys interface {
	SessionKey() *keyring.Key
}

type SessionStoreDependencies struct {
	Role    Role
	Path    string
	Keys    SessionKeys
	Writer  SecretWriter
	Reader  FileReader
	Remover FileRemover
	Random  io.Reader
}

type SessionStore struct {
	aad     []byte
	path    string
	keys    SessionKeys
	writer  SecretWriter
	reader  FileReader
	remover FileRemover

	randMu sync.Mutex
	random io.Reader

	mu sync.Mutex
}

type sealedSession struct {
	Version    int    `json:"v"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ct"`
}

func NewSessionStore(deps SessionStoreDependencies) (*SessionStore, error) {
	missing := []struct {
		name string
		nil  bool
	}{
		{"keys", deps.Keys == nil},
		{"writer", deps.Writer == nil},
		{"reader", deps.Reader == nil},
		{"remover", deps.Remover == nil},
		{"random", deps.Random == nil},
		{"path", deps.Path == ""},
	}
	for _, dep := range missing {
		if dep.nil {
			return nil, fmt.Errorf("identity: session store: %w: %s", errs.ErrMissingDependency, dep.name)
		}
	}
	if !deps.Role.valid() {
		return nil, fmt.Errorf("identity: session store: invalid role %q", string(deps.Role))
	}
	return &SessionStore{
		aad:     []byte(sessionAADPrefix + string(deps.Role)),
		path:    deps.Path,
		keys:    deps.Keys,
		writer:  deps.Writer,
		reader:  deps.Reader,
		remover: deps.Remover,
		random:  deps.Random,
	}, nil
}

func (s *SessionStore) aeadForSession() (cipher.AEAD, error) {
	key := s.keys.SessionKey()
	if key == nil {
		return nil, ErrSessionKeyMissing
	}
	defer key.Clear()
	aead, err := chacha20poly1305.NewX(key.Bytes())
	if err != nil {
		return nil, fmt.Errorf("identity: session cipher: %w", err)
	}
	return aead, nil
}

func (s *SessionStore) LoadSession(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := s.reader.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("identity: load session: %w", errors.Join(ErrSessionNotFound, fs.ErrNotExist))
	}
	if err != nil {
		return nil, fmt.Errorf("identity: load session: read: %w", err)
	}
	var envelope sealedSession
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Version != sealedSessionVersion {
		return nil, fmt.Errorf("identity: load session: %w", ErrSessionUnseal)
	}
	nonce, nonceErr := base64.StdEncoding.DecodeString(envelope.Nonce)
	sealed, sealedErr := base64.StdEncoding.DecodeString(envelope.Ciphertext)
	if nonceErr != nil || sealedErr != nil || len(nonce) != chacha20poly1305.NonceSizeX {
		return nil, fmt.Errorf("identity: load session: %w", ErrSessionUnseal)
	}
	aead, err := s.aeadForSession()
	if err != nil {
		return nil, fmt.Errorf("identity: load session: %w", err)
	}
	plain, err := aead.Open(nil, nonce, sealed, s.aad)
	if err != nil {
		return nil, fmt.Errorf("identity: load session: %w", ErrSessionUnseal)
	}
	return plain, nil
}

func (s *SessionStore) StoreSession(ctx context.Context, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(data) == 0 {
		return errors.New("identity: store session: empty session")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	aead, err := s.aeadForSession()
	if err != nil {
		return fmt.Errorf("identity: store session: %w", err)
	}
	nonce := make([]byte, aead.NonceSize())
	if err := s.readRandom(nonce); err != nil {
		return err
	}
	sealed := aead.Seal(nil, nonce, data, s.aad)
	encoded, err := json.Marshal(sealedSession{
		Version:    sealedSessionVersion,
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
		Ciphertext: base64.StdEncoding.EncodeToString(sealed),
	})
	if err != nil {
		return fmt.Errorf("identity: store session: %w", err)
	}
	if err := s.writer.Write(s.path, encoded); err != nil {
		return fmt.Errorf("identity: store session: %w", err)
	}
	return nil
}

func (s *SessionStore) Delete(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.remover.Remove(s.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("identity: delete session: %w", err)
	}
	return nil
}

func (s *SessionStore) readRandom(dst []byte) error {
	s.randMu.Lock()
	defer s.randMu.Unlock()
	if _, err := io.ReadFull(s.random, dst); err != nil {
		return fmt.Errorf("identity: store session: read nonce: %w", err)
	}
	return nil
}
