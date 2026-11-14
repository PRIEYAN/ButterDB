package rebuild

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/PRIEYAN/ButterDB/internal/projection"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

var ErrEncryptionConfigReplayInvalid = errors.New("rebuild: encryption config replay is invalid")

type encryptionReplayRow struct {
	replayRow
	storedHash string
	tampered   bool
}

func (r *Rebuilder) RebuildEncryptionConfig(ctx context.Context, w projection.Writer, channelID int64) (bool, error) {
	if w == nil || channelID <= 0 {
		return false, fmt.Errorf("%w: writer and channel are required", ErrEncryptionConfigReplayInvalid)
	}
	var found bool
	err := w.Write(ctx, func(tx *sql.Tx) error {
		var err error
		found, err = r.RebuildEncryptionConfigTx(ctx, tx, channelID)
		return err
	})
	if err != nil {
		return false, err
	}
	return found, nil
}

func (r *Rebuilder) RebuildEncryptionConfigTx(ctx context.Context, tx projection.Tx, channelID int64) (bool, error) {
	if channelID <= 0 {
		return false, fmt.Errorf("%w: channel is required", ErrEncryptionConfigReplayInvalid)
	}
	replay, err := readEncryptionReplay(ctx, tx, channelID)
	if err != nil {
		return false, err
	}
	if len(replay) == 0 {
		return false, nil
	}
	if err := verifyEncryptionReplay(replay); err != nil {
		return false, err
	}
	if err := clearEncryptionState(ctx, tx, channelID); err != nil {
		return false, err
	}
	for _, row := range replay {
		if _, err := r.applyRow(ctx, tx, channelID, row.replayRow); err != nil {
			return false, fmt.Errorf("%w: apply msg %d: %w", ErrEncryptionConfigReplayInvalid, row.msgID, err)
		}
	}
	r.logger.Info("rebuild: encryption config rebuilt from replay", "channel_id", channelID, "ops_replayed", len(replay))
	return true, nil
}

func verifyEncryptionReplay(replay []encryptionReplayRow) error {
	for _, row := range replay {
		if row.tampered || hashHeader(row.rawHeader) != row.storedHash {
			return fmt.Errorf("%w: integrity check failed for msg %d", ErrEncryptionConfigReplayInvalid, row.msgID)
		}
		op, err := wire.Parse(row.rawHeader)
		if err != nil || op.Type != wire.OpEncConfig {
			return fmt.Errorf("%w: parse msg %d", ErrEncryptionConfigReplayInvalid, row.msgID)
		}
	}
	return nil
}

func hashHeader(rawHeader string) string {
	sum := sha256.Sum256([]byte(rawHeader))
	return hex.EncodeToString(sum[:])
}

func clearEncryptionState(ctx context.Context, tx projection.Tx, channelID int64) error {
	statements := []string{
		`DELETE FROM replay_log_rejects WHERE channel_id = ?1 AND msg_id IN (SELECT msg_id FROM replay_log WHERE channel_id = ?1 AND op_type = 'encfg')`,
		`DELETE FROM projection_operations WHERE channel_id = ?1 AND op_type = 'encfg'`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement, channelID); err != nil {
			return fmt.Errorf("%w: clear derived policy: %w", ErrEncryptionConfigReplayInvalid, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM encryption_keys`); err != nil {
		return fmt.Errorf("%w: clear derived policy: %w", ErrEncryptionConfigReplayInvalid, err)
	}
	return nil
}

func readEncryptionReplay(ctx context.Context, tx projection.Tx, channelID int64) ([]encryptionReplayRow, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT r.msg_id, r.op_type, r.raw_header, r.is_document, r.actor_user_id, r.msg_date, r.first_seen_hash,
		       EXISTS (SELECT 1 FROM replay_log_tamper AS t WHERE t.channel_id = r.channel_id AND t.msg_id = r.msg_id)
		FROM replay_log AS r
		WHERE r.channel_id = ? AND r.op_type = ?
		ORDER BY r.msg_id ASC
	`, channelID, string(wire.OpEncConfig))
	if err != nil {
		return nil, fmt.Errorf("%w: scan: %w", ErrEncryptionConfigReplayInvalid, err)
	}
	defer rows.Close()
	var replay []encryptionReplayRow
	for rows.Next() {
		var row encryptionReplayRow
		var isDocument, tampered int
		err := rows.Scan(&row.msgID, &row.opType, &row.rawHeader, &isDocument, &row.actorUserID, &row.msgDate, &row.storedHash, &tampered)
		if err != nil {
			return nil, fmt.Errorf("%w: row: %w", ErrEncryptionConfigReplayInvalid, err)
		}
		row.isDocument = isDocument != 0
		row.tampered = tampered != 0
		replay = append(replay, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: rows: %w", ErrEncryptionConfigReplayInvalid, err)
	}
	return replay, nil
}
