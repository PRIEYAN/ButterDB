package jobrepo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/PRIEYAN/ButterDB/internal/domain/limits"
)

const insertIdempotency = `INSERT INTO idempotency_keys (scope, idempotency_key, bucket_channel_id, object_key, body_sha256, job_id, created_at, expires_at)
VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8)
ON CONFLICT (scope, idempotency_key) DO UPDATE SET
  bucket_channel_id = excluded.bucket_channel_id, object_key = excluded.object_key, body_sha256 = excluded.body_sha256,
  job_id = excluded.job_id, created_at = excluded.created_at, expires_at = excluded.expires_at
WHERE (SELECT state FROM upload_jobs WHERE job_id = idempotency_keys.job_id) = 'aborted'
   OR expires_at <= excluded.created_at`

const selectIdempotency = `SELECT scope, idempotency_key, bucket_channel_id, object_key, body_sha256, job_id, created_at, expires_at
FROM idempotency_keys WHERE scope = ?1 AND idempotency_key = ?2`

func (r *Repository) CreateUploadWithIdempotency(ctx context.Context, job UploadJob, record IdempotencyRecord) error {
	err := r.db.Write(ctx, func(tx *sql.Tx) error {
		if err := r.insertUploadRow(ctx, tx, job); err != nil {
			return err
		}
		return r.insertIdempotencyRow(ctx, tx, job, record)
	})
	if err != nil {
		return fmt.Errorf("jobrepo: create upload %s with idempotency: %w", job.JobID, err)
	}
	return nil
}

func (r *Repository) insertIdempotencyRow(ctx context.Context, tx *sql.Tx, job UploadJob, record IdempotencyRecord) error {
	if record.Scope != job.Scope {
		return errors.New("idempotency scope must equal the job scope")
	}
	now := r.nowMillis()
	expiresAt := record.ExpiresAt
	if expiresAt == 0 {
		expiresAt = now + limits.IdempotencyTTL.Milliseconds()
	}
	result, err := tx.ExecContext(ctx, insertIdempotency, record.Scope, record.IdempotencyKey, record.BucketChannelID,
		record.ObjectKey, record.BodySHA256, job.JobID, now, expiresAt)
	if err != nil {
		return err
	}
	written, err := affectedOne(result)
	if err != nil {
		return err
	}
	if !written {
		return ErrDuplicateIdempotencyKey
	}
	return nil
}

func (r *Repository) GetIdempotency(ctx context.Context, scope, idempotencyKey string) (IdempotencyRecord, error) {
	var record IdempotencyRecord
	err := r.db.Read(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, selectIdempotency, scope, idempotencyKey).Scan(&record.Scope, &record.IdempotencyKey,
			&record.BucketChannelID, &record.ObjectKey, &record.BodySHA256, &record.JobID, &record.CreatedAt, &record.ExpiresAt)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrIdempotencyNotFound
		}
		return err
	})
	if err != nil {
		return IdempotencyRecord{}, fmt.Errorf("jobrepo: get idempotency record: %w", err)
	}
	return record, nil
}

func (r *Repository) DeleteExpiredIdempotency(ctx context.Context, now int64) (int, error) {
	var removed int64
	err := r.db.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "DELETE FROM idempotency_keys WHERE expires_at <= ?1", now)
		if err != nil {
			return err
		}
		removed, err = result.RowsAffected()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("jobrepo: delete expired idempotency records: %w", err)
	}
	return int(removed), nil
}

func (r *Repository) PruneTerminalJobs(ctx context.Context, updatedBefore int64) (int, error) {
	var removed int64
	err := r.db.Write(ctx, func(tx *sql.Tx) error {
		for _, table := range []string{"upload_jobs", "delete_jobs"} {
			result, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE state IN ('committed','aborted') AND updated_at < ?1", updatedBefore)
			if err != nil {
				return err
			}
			count, err := result.RowsAffected()
			if err != nil {
				return err
			}
			removed += count
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("jobrepo: prune terminal jobs: %w", err)
	}
	return int(removed), nil
}
