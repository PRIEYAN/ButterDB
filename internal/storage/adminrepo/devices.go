package adminrepo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const deviceColumns = `device_id, name, platform, client_version, created_via, token_hash, token_issued_at,
  prev_token_hash, prev_valid_until, created_at, last_seen_at, last_seen_ip, revoked_at, revoked_by`

const (
	selectDevice = `SELECT ` + deviceColumns + ` FROM admin_devices WHERE device_id = ?1`
	listDevices  = `SELECT ` + deviceColumns + ` FROM admin_devices
WHERE revoked_at IS NULL OR revoked_at >= ?1 ORDER BY created_at, device_id`
	insertDevice = `INSERT INTO admin_devices (device_id, name, platform, client_version, created_via, token_hash,
  token_issued_at, created_at, last_seen_at, last_seen_ip)
VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?7, ?7, ?8)`
	rotateDevice = `UPDATE admin_devices SET prev_token_hash = token_hash, prev_valid_until = ?3,
  token_hash = ?2, token_issued_at = ?4 WHERE device_id = ?1`
	clearPrevToken = `UPDATE admin_devices SET prev_token_hash = NULL, prev_valid_until = NULL WHERE device_id = ?1`
	revokeDevice   = `UPDATE admin_devices SET revoked_at = ?2, revoked_by = ?3
WHERE device_id = ?1 AND revoked_at IS NULL`
	touchDevice  = `UPDATE admin_devices SET last_seen_at = ?2, last_seen_ip = ?3 WHERE device_id = ?1`
	renameDevice = `UPDATE admin_devices SET name = ?2 WHERE device_id = ?1`
	revokeIdle   = `UPDATE admin_devices SET revoked_at = ?2, revoked_by = ?3
WHERE revoked_at IS NULL AND last_seen_at < ?1`
	purgeRevoked = `DELETE FROM admin_devices WHERE revoked_at IS NOT NULL AND revoked_at < ?1
AND (EXISTS (SELECT 1 FROM admin_devices WHERE revoked_at IS NULL)
  OR device_id != (SELECT device_id FROM admin_devices ORDER BY revoked_at DESC, device_id LIMIT 1))`
)

const RevokedByIdleSweeper = "system:idle"

type rowScanner interface {
	Scan(dest ...any) error
}

func scanDevice(row rowScanner) (Device, error) {
	var (
		d         Device
		via       string
		tokenHash []byte
		issued    int64
		prevHash  []byte
		prevUntil sql.NullInt64
		created   int64
		lastSeen  int64
		revokedAt sql.NullInt64
		revokedBy sql.NullString
	)
	err := row.Scan(&d.DeviceID, &d.Name, &d.Platform, &d.ClientVersion, &via, &tokenHash, &issued,
		&prevHash, &prevUntil, &created, &lastSeen, &d.LastSeenIP, &revokedAt, &revokedBy)
	if err != nil {
		return Device{}, err
	}
	d.CreatedVia = CreatedVia(via)
	if d.TokenHash, err = copyHash(tokenHash); err != nil {
		return Device{}, err
	}
	d.TokenIssuedAt = fromMillis(issued)
	d.CreatedAt = fromMillis(created)
	d.LastSeenAt = fromMillis(lastSeen)
	if prevHash != nil {
		if d.PrevTokenHash, err = copyHash(prevHash); err != nil {
			return Device{}, err
		}
		d.HasPrevToken = true
		d.PrevValidUntil = fromMillis(prevUntil.Int64)
	}
	if revokedAt.Valid {
		d.RevokedAt = fromMillis(revokedAt.Int64)
		d.RevokedBy = revokedBy.String
	}
	return d, nil
}

func loadDevice(ctx context.Context, tx *sql.Tx, id string) (Device, error) {
	d, err := scanDevice(tx.QueryRowContext(ctx, selectDevice, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, ErrDeviceNotFound
	}
	return d, err
}

func insertDeviceTx(ctx context.Context, tx *sql.Tx, d NewDevice) error {
	_, err := tx.ExecContext(ctx, insertDevice, d.DeviceID, d.Name, d.Platform, d.ClientVersion,
		string(d.CreatedVia), d.TokenHash[:], millis(d.CreatedAt), d.LastSeenIP)
	if isUniqueViolation(err) {
		return ErrDeviceExists
	}
	return err
}

func (r *Repository) Device(ctx context.Context, id string) (Device, error) {
	var d Device
	err := r.db.Read(ctx, func(tx *sql.Tx) error {
		var err error
		d, err = loadDevice(ctx, tx, id)
		return err
	})
	if err != nil {
		return Device{}, fmt.Errorf("adminrepo: device: %w", err)
	}
	return d, nil
}

func (r *Repository) InsertDevice(ctx context.Context, d NewDevice) error {
	err := r.db.Write(ctx, func(tx *sql.Tx) error {
		return insertDeviceTx(ctx, tx, d)
	})
	if err != nil {
		return fmt.Errorf("adminrepo: insert device: %w", err)
	}
	return nil
}

func (r *Repository) ListDevices(ctx context.Context, includeRevokedSince time.Time) ([]Device, error) {
	devices := []Device{}
	err := r.db.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, listDevices, millis(includeRevokedSince))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d, err := scanDevice(rows)
			if err != nil {
				return err
			}
			devices = append(devices, d)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("adminrepo: list devices: %w", err)
	}
	return devices, nil
}

func (r *Repository) ActiveDeviceCount(ctx context.Context) (int, error) {
	var count int
	err := r.db.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM admin_devices WHERE revoked_at IS NULL`).Scan(&count)
	})
	if err != nil {
		return 0, fmt.Errorf("adminrepo: count active devices: %w", err)
	}
	return count, nil
}

func (r *Repository) RenameDevice(ctx context.Context, id, name string) error {
	err := r.db.Write(ctx, func(tx *sql.Tx) error {
		return requireAffected(tx.ExecContext(ctx, renameDevice, id, name))
	})
	if err != nil {
		return fmt.Errorf("adminrepo: rename device: %w", err)
	}
	return nil
}

func (r *Repository) Rotate(ctx context.Context, id string, newHash [32]byte, now time.Time) error {
	err := r.db.Write(ctx, func(tx *sql.Tx) error {
		current, err := loadDevice(ctx, tx, id)
		if err != nil {
			return err
		}
		if !current.RevokedAt.IsZero() {
			return ErrDeviceRevoked
		}
		_, err = tx.ExecContext(ctx, rotateDevice, id, newHash[:], millis(now.Add(PrevTokenGrace)), millis(now))
		return err
	})
	if err != nil {
		return fmt.Errorf("adminrepo: rotate device: %w", err)
	}
	return nil
}

func (r *Repository) MarkNewTokenUsed(ctx context.Context, id string) error {
	err := r.db.Write(ctx, func(tx *sql.Tx) error {
		return requireAffected(tx.ExecContext(ctx, clearPrevToken, id))
	})
	if err != nil {
		return fmt.Errorf("adminrepo: mark new token used: %w", err)
	}
	return nil
}

func (r *Repository) Revoke(ctx context.Context, id string, now time.Time) error {
	err := r.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := loadDevice(ctx, tx, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, revokeDevice, id, millis(now), "device")
		return err
	})
	if err != nil {
		return fmt.Errorf("adminrepo: revoke device: %w", err)
	}
	return nil
}

func (r *Repository) TouchSeen(ctx context.Context, id string, ip string, now time.Time) error {
	err := r.db.Write(ctx, func(tx *sql.Tx) error {
		return requireAffected(tx.ExecContext(ctx, touchDevice, id, millis(now), ip))
	})
	if err != nil {
		return fmt.Errorf("adminrepo: touch device: %w", err)
	}
	return nil
}

func (r *Repository) RevokeIdle(ctx context.Context, before time.Time) (int, error) {
	var revoked int64
	err := r.db.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, revokeIdle, millis(before), millis(r.clock.Now()), RevokedByIdleSweeper)
		if err != nil {
			return err
		}
		revoked, err = result.RowsAffected()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("adminrepo: revoke idle devices: %w", err)
	}
	return int(revoked), nil
}

func (r *Repository) DeleteRevokedBefore(ctx context.Context, cutoff time.Time) (int, error) {
	var deleted int64
	err := r.db.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, purgeRevoked, millis(cutoff))
		if err != nil {
			return err
		}
		deleted, err = result.RowsAffected()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("adminrepo: delete revoked devices: %w", err)
	}
	return int(deleted), nil
}

func requireAffected(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrDeviceNotFound
	}
	return nil
}
