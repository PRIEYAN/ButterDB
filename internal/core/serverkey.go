package core

import (
	cryptorand "crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/PRIEYAN/ButterDB/internal/crypto/keyring"
)

const keyFingerprintPrefix = "bkf1_"

func (a *App) ensureServerKey() error {
	path := a.cfg.ServerKeyFile
	if fileExists(path) {
		return nil
	}
	if fileExists(a.DataDir().Join(stateFileName)) {
		return keyFailure(ReasonKeyMissing, "the deployment exists but its server key file is missing")
	}
	fingerprint, err := keyring.Generate(path, cryptorand.Reader)
	if err != nil {
		return fmt.Errorf("%w: generate server key: %w", ErrKeyOrRegistry, err)
	}
	a.logger.Warn("server key generated", "component", "lifecycle", "path", path,
		"kek_fingerprint", keyFingerprintPrefix+hex.EncodeToString(fingerprint[:]), "action", "back this file up off the host now")
	return nil
}
