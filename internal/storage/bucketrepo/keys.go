package bucketrepo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

var ErrCorruptKeyRow = errors.New("bucketrepo: corrupt encryption key row")

const (
	keyIDLength          = 16
	wrappedDEKBytes      = 72
	keyCheckBytes        = 61
	fingerprintLength    = 37
	fingerprintPrefix    = "bkf1_"
	minimumConfigVersion = 1
)

type EncryptionKey struct {
	KeyID          string
	WrappedDEK     []byte
	KeyCheck       []byte
	KEKFingerprint string
	ConfigVersion  int
	FirstMsgID     int64
	MsgID          int64
}

const selectKey = `SELECT key_id, wrapped_dek, key_check, kek_fingerprint, config_version, first_msg_id, msg_id
FROM encryption_keys WHERE bucket_channel_id = ?1`

const (
	selectActiveKey = selectKey + ` AND kek_fingerprint = ?2 ORDER BY first_msg_id DESC LIMIT 1`
	selectKeyByID   = selectKey + ` AND key_id = ?2`
)

func (r *Repository) ActiveKey(ctx context.Context, channelID int64, kekFingerprint string) (EncryptionKey, bool, error) {
	key, found, err := r.lookupKey(ctx, selectActiveKey, channelID, kekFingerprint)
	if err != nil {
		return EncryptionKey{}, false, fmt.Errorf("bucketrepo: active key of channel %d: %w", channelID, err)
	}
	return key, found, nil
}

func (r *Repository) Key(ctx context.Context, channelID int64, keyID string) (EncryptionKey, bool, error) {
	key, found, err := r.lookupKey(ctx, selectKeyByID, channelID, keyID)
	if err != nil {
		return EncryptionKey{}, false, fmt.Errorf("bucketrepo: key of channel %d: %w", channelID, err)
	}
	return key, found, nil
}

func (r *Repository) lookupKey(ctx context.Context, statement string, channelID int64, arg string) (EncryptionKey, bool, error) {
	var key EncryptionKey
	found := true
	err := r.db.Read(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, statement, channelID, arg)
		err := row.Scan(&key.KeyID, &key.WrappedDEK, &key.KeyCheck, &key.KEKFingerprint, &key.ConfigVersion, &key.FirstMsgID, &key.MsgID)
		if errors.Is(err, sql.ErrNoRows) {
			found = false
			return nil
		}
		return err
	})
	if err != nil {
		return EncryptionKey{}, false, err
	}
	if !found {
		return EncryptionKey{}, false, nil
	}
	if err := validateKey(key); err != nil {
		return EncryptionKey{}, false, err
	}
	return key, true, nil
}

func validateKey(key EncryptionKey) error {
	switch {
	case len(key.KeyID) != keyIDLength:
		return fmt.Errorf("%w: key id is %d bytes", ErrCorruptKeyRow, len(key.KeyID))
	case len(key.WrappedDEK) != wrappedDEKBytes:
		return fmt.Errorf("%w: wrapped key is %d bytes", ErrCorruptKeyRow, len(key.WrappedDEK))
	case len(key.KeyCheck) != keyCheckBytes:
		return fmt.Errorf("%w: key check is %d bytes", ErrCorruptKeyRow, len(key.KeyCheck))
	case len(key.KEKFingerprint) != fingerprintLength || !strings.HasPrefix(key.KEKFingerprint, fingerprintPrefix):
		return fmt.Errorf("%w: malformed fingerprint", ErrCorruptKeyRow)
	case key.ConfigVersion < minimumConfigVersion:
		return fmt.Errorf("%w: config version %d", ErrCorruptKeyRow, key.ConfigVersion)
	}
	return nil
}
