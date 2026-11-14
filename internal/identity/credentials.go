package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"strings"
)

const maxAPIID = 2147483647

const apiHashLength = 32

type Credentials struct {
	APIID   int
	APIHash string
}

func (c Credentials) LogValue() slog.Value {
	return slog.StringValue("[REDACTED]")
}

type credentialsFile struct {
	APIID   int    `json:"api_id"`
	APIHash string `json:"api_hash"`
}

func validateCredentials(apiID int, apiHash string) (Credentials, error) {
	hash := strings.ToLower(strings.TrimSpace(apiHash))
	if apiID < 1 || apiID > maxAPIID || len(hash) != apiHashLength {
		return Credentials{}, fmt.Errorf("identity: save credentials: %w", ErrCredentialsInvalid)
	}
	for i := 0; i < len(hash); i++ {
		c := hash[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return Credentials{}, fmt.Errorf("identity: save credentials: %w", ErrCredentialsInvalid)
		}
	}
	return Credentials{APIID: apiID, APIHash: hash}, nil
}

func (s *Service) LoadCredentials(ctx context.Context) (Credentials, error) {
	if err := ctx.Err(); err != nil {
		return Credentials{}, err
	}
	raw, err := s.reader.ReadFile(s.credentialsPath)
	if errors.Is(err, fs.ErrNotExist) {
		return Credentials{}, fmt.Errorf("identity: load credentials: %w", ErrCredentialsMissing)
	}
	if err != nil {
		return Credentials{}, fmt.Errorf("identity: load credentials: %w", errors.Join(ErrCredentialsCorrupt, err))
	}
	defer clear(raw)
	var file credentialsFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return Credentials{}, fmt.Errorf("identity: load credentials: %w", ErrCredentialsCorrupt)
	}
	if file.APIID < 1 || file.APIHash == "" {
		return Credentials{}, fmt.Errorf("identity: load credentials: %w", ErrCredentialsCorrupt)
	}
	return Credentials{APIID: file.APIID, APIHash: file.APIHash}, nil
}

func (s *Service) SaveCredentials(ctx context.Context, apiID int, apiHash string) error {
	creds, err := validateCredentials(apiID, apiHash)
	if err != nil {
		return err
	}
	if err := s.ensureCredentialsEditable(ctx); err != nil {
		return err
	}
	raw, err := json.Marshal(credentialsFile{APIID: creds.APIID, APIHash: creds.APIHash})
	if err != nil {
		return fmt.Errorf("identity: save credentials: %w", err)
	}
	defer clear(raw)
	if err := s.writer.Write(s.credentialsPath, raw); err != nil {
		return fmt.Errorf("identity: save credentials: %w", err)
	}
	return nil
}

func (s *Service) DeleteCredentials(ctx context.Context) error {
	if err := s.ensureCredentialsEditable(ctx); err != nil {
		return err
	}
	if err := s.remover.Remove(s.credentialsPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("identity: delete credentials: %w", err)
	}
	return nil
}

func (s *Service) ensureCredentialsEditable(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.attemptActive() {
		return fmt.Errorf("identity: credentials: %w", ErrLoginInProgress)
	}
	linked, err := s.isLinked(ctx)
	if err != nil {
		return err
	}
	if linked {
		return fmt.Errorf("identity: credentials: %w", ErrAlreadyLinked)
	}
	return nil
}
