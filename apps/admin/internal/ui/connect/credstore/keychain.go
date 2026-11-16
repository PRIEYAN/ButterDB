package credstore

import (
	"errors"
	"fmt"
	"strings"

	"github.com/zalando/go-keyring"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

const KeychainService = "butterdb-admin"

var unavailableMarkers = []string{"dbus", "org.freedesktop", "session bus", "secret service"}

type Keychain struct{}

func NewKeychain() *Keychain {
	return &Keychain{}
}

func (k *Keychain) Get(serverID string) (string, error) {
	token, err := keyring.Get(KeychainService, serverID)
	if err != nil {
		return "", mapKeychainError("get", err)
	}
	return token, nil
}

func (k *Keychain) Set(serverID, token string) error {
	if err := keyring.Set(KeychainService, serverID, token); err != nil {
		return mapKeychainError("set", err)
	}
	return nil
}

func (k *Keychain) Delete(serverID string) error {
	if err := keyring.Delete(KeychainService, serverID); err != nil {
		return mapKeychainError("delete", err)
	}
	return nil
}

func (k *Keychain) Kind() model.TokenStorage {
	return model.TokenKeychain
}

func mapKeychainError(op string, err error) error {
	switch {
	case errors.Is(err, keyring.ErrNotFound):
		return fmt.Errorf("credstore: keychain %s: %w", op, model.ErrTokenNotFound)
	case errors.Is(err, keyring.ErrUnsupportedPlatform), looksUnavailable(err):
		return fmt.Errorf("credstore: keychain %s: %w", op, model.ErrKeychainUnavailable)
	default:
		return fmt.Errorf("credstore: keychain %s: %w", op, err)
	}
}

func looksUnavailable(err error) bool {
	text := strings.ToLower(err.Error())
	for _, marker := range unavailableMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}
