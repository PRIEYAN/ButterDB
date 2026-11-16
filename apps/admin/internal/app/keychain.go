package app

import "github.com/PRIEYAN/ButterDB/apps/admin/internal/model"

const (
	probeAccount = "butterdb-admin-probe"
	probeSecret  = "probe"
)

type unavailableTokens struct{}

func (unavailableTokens) Get(string) (string, error) { return "", model.ErrKeychainUnavailable }

func (unavailableTokens) Set(string, string) error { return model.ErrKeychainUnavailable }

func (unavailableTokens) Delete(string) error { return model.ErrKeychainUnavailable }

func (unavailableTokens) Kind() model.TokenStorage { return model.TokenKeychain }

func ProbeKeychain(keychain model.TokenStore) model.TokenStore {
	if keychain == nil {
		return unavailableTokens{}
	}
	if err := keychain.Set(probeAccount, probeSecret); err != nil {
		return unavailableTokens{}
	}
	_ = keychain.Delete(probeAccount)
	return keychain
}

func keychainUsable(tokens model.TokenStore) bool {
	_, unavailable := tokens.(unavailableTokens)
	return !unavailable
}
