package adminrepo

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"
)

const (
	selectSetupToken  = `SELECT token_hash, expires_at FROM admin_setup_token WHERE id = 1`
	replaceSetupToken = `INSERT INTO admin_setup_token (id, token_hash, created_at, expires_at) VALUES (1, ?1, ?2, ?3)
ON CONFLICT(id) DO UPDATE SET token_hash = excluded.token_hash, created_at = excluded.created_at, expires_at = excluded.expires_at`
	replacePairingCode = `INSERT INTO admin_pairing_codes (delivery, code_hash, device_name, created_at, expires_at)
VALUES (?1, ?2, '', ?3, ?4)
ON CONFLICT(delivery) DO UPDATE SET code_hash = excluded.code_hash, device_name = excluded.device_name,
  created_at = excluded.created_at, expires_at = excluded.expires_at`
	selectPairingCodes = `SELECT delivery, code_hash, expires_at FROM admin_pairing_codes`
)

func (r *Repository) ReplaceSetupToken(ctx context.Context, hash [32]byte, expiresAt time.Time) error {
	err := r.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, replaceSetupToken, hash[:], millis(r.clock.Now()), millis(expiresAt))
		return err
	})
	if err != nil {
		return fmt.Errorf("adminrepo: replace setup token: %w", err)
	}
	return nil
}

func (r *Repository) ConsumeSetupTokenAndAddDevice(ctx context.Context, hash [32]byte, now time.Time, dev NewDevice) error {
	err := r.db.Write(ctx, func(tx *sql.Tx) error {
		claimed, err := hasDevice(ctx, tx)
		if err != nil {
			return err
		}
		if claimed {
			return ErrAlreadyClaimed
		}
		if err := matchSetupToken(ctx, tx, hash, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM admin_setup_token WHERE id = 1`); err != nil {
			return err
		}
		dev.CreatedVia = CreatedViaSetup
		return insertDeviceTx(ctx, tx, dev)
	})
	if err != nil {
		return fmt.Errorf("adminrepo: consume setup token: %w", err)
	}
	return nil
}

func matchSetupToken(ctx context.Context, tx *sql.Tx, hash [32]byte, now time.Time) error {
	var (
		stored    []byte
		expiresAt int64
	)
	err := tx.QueryRowContext(ctx, selectSetupToken).Scan(&stored, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrSetupTokenInvalid
	}
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare(stored, hash[:]) != 1 || expiresAt <= millis(now) {
		return ErrSetupTokenInvalid
	}
	return nil
}

func (r *Repository) ReplacePairingCode(ctx context.Context, delivery string, hash [32]byte, expiresAt time.Time) error {
	if delivery != deliverySavedMessage && delivery != deliveryHost {
		return fmt.Errorf("adminrepo: replace pairing code: unknown delivery %q", delivery)
	}
	err := r.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, replacePairingCode, delivery, hash[:], millis(r.clock.Now()), millis(expiresAt))
		return err
	})
	if err != nil {
		return fmt.Errorf("adminrepo: replace pairing code: %w", err)
	}
	return nil
}

func (r *Repository) DeletePairingCode(ctx context.Context, delivery string) error {
	err := r.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM admin_pairing_codes WHERE delivery = ?1`, delivery)
		return err
	})
	if err != nil {
		return fmt.Errorf("adminrepo: delete pairing code: %w", err)
	}
	return nil
}

func (r *Repository) ConsumePairingCode(ctx context.Context, hash [32]byte, now time.Time, d NewDevice) error {
	err := r.db.Write(ctx, func(tx *sql.Tx) error {
		claimed, err := hasDevice(ctx, tx)
		if err != nil {
			return err
		}
		if !claimed {
			return ErrNotClaimed
		}
		delivery, err := matchPairingCode(ctx, tx, hash, now)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM admin_pairing_codes WHERE delivery = ?1`, delivery); err != nil {
			return err
		}
		d.CreatedVia = createdViaFor(delivery)
		if err := insertDeviceTx(ctx, tx, d); err != nil {
			return err
		}
		return writeMeta(ctx, tx, MetaPairingFailures, "0")
	})
	if err != nil {
		return fmt.Errorf("adminrepo: consume pairing code: %w", err)
	}
	return nil
}

func createdViaFor(delivery string) CreatedVia {
	if delivery == deliveryHost {
		return CreatedViaPairingHost
	}
	return CreatedViaPairingSavedMessages
}

func matchPairingCode(ctx context.Context, tx *sql.Tx, hash [32]byte, now time.Time) (string, error) {
	rows, err := tx.QueryContext(ctx, selectPairingCodes)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	matched := ""
	for rows.Next() {
		var (
			delivery  string
			stored    []byte
			expiresAt int64
		)
		if err := rows.Scan(&delivery, &stored, &expiresAt); err != nil {
			return "", err
		}
		equal := subtle.ConstantTimeCompare(stored, hash[:]) == 1
		if equal && expiresAt > millis(now) {
			matched = delivery
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if matched == "" {
		return "", ErrPairingCodeInvalid
	}
	return matched, nil
}

func (r *Repository) RecordPairingFailure(ctx context.Context) (int, error) {
	var consecutive int
	err := r.db.Write(ctx, func(tx *sql.Tx) error {
		value, _, err := readMeta(ctx, tx, MetaPairingFailures)
		if err != nil {
			return err
		}
		previous, _ := strconv.Atoi(value)
		consecutive = previous + 1
		return writeMeta(ctx, tx, MetaPairingFailures, strconv.Itoa(consecutive))
	})
	if err != nil {
		return 0, fmt.Errorf("adminrepo: record pairing failure: %w", err)
	}
	return consecutive, nil
}

func (r *Repository) InvalidatePairingCodes(ctx context.Context) error {
	err := r.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM admin_pairing_codes`); err != nil {
			return err
		}
		return writeMeta(ctx, tx, MetaPairingFailures, "0")
	})
	if err != nil {
		return fmt.Errorf("adminrepo: invalidate pairing codes: %w", err)
	}
	return nil
}

func (r *Repository) DeleteExpiredSecrets(ctx context.Context, now time.Time) (int, error) {
	var deleted int
	err := r.db.Write(ctx, func(tx *sql.Tx) error {
		for _, statement := range []string{
			`DELETE FROM admin_setup_token WHERE expires_at <= ?1`,
			`DELETE FROM admin_pairing_codes WHERE expires_at <= ?1`,
		} {
			result, err := tx.ExecContext(ctx, statement, millis(now))
			if err != nil {
				return err
			}
			affected, err := result.RowsAffected()
			if err != nil {
				return err
			}
			deleted += int(affected)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("adminrepo: delete expired secrets: %w", err)
	}
	return deleted, nil
}
