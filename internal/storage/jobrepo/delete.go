package jobrepo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const deleteColumns = `job_id, origin, scope, writer, request_id, bucket_channel_id, bucket_name, object_key, state,
  target_put_msg_id, del_header, del_msg_id, error_code, attempts, created_at, updated_at`

const insertDelete = `INSERT INTO delete_jobs (` + deleteColumns + `)
VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12, ?13, ?14, ?15, ?16)`

const transitionDelete = `UPDATE delete_jobs SET
  state = ?3,
  del_header = COALESCE(?4, del_header),
  del_msg_id = COALESCE(?5, del_msg_id),
  error_code = COALESCE(?6, error_code),
  attempts = attempts + ?7,
  updated_at = ?8
WHERE job_id = ?1 AND state = ?2`

func (r *Repository) CreateDelete(ctx context.Context, job DeleteJob) error {
	err := r.db.Write(ctx, func(tx *sql.Tx) error {
		if job.State != DeletePrepared {
			return fmt.Errorf("new delete must start prepared: %w", ErrInvalidTransition)
		}
		if !validScope(job.Scope) {
			return errors.New("scope must be key:<id> or device:<id>")
		}
		now := r.nowMillis()
		_, err := tx.ExecContext(ctx, insertDelete, job.JobID, string(job.Origin), job.Scope, job.Writer, job.RequestID,
			job.BucketChannelID, job.BucketName, job.ObjectKey, string(job.State), job.TargetPutMsgID, job.DelHeader,
			job.DelMsgID, job.ErrorCode, job.Attempts, now, now)
		if isUniqueViolation(err) {
			return ErrDuplicateJob
		}
		return err
	})
	if err != nil {
		return fmt.Errorf("jobrepo: create delete %s: %w", job.JobID, err)
	}
	return nil
}

func (r *Repository) GetDelete(ctx context.Context, jobID string) (DeleteJob, error) {
	var job DeleteJob
	err := r.db.Read(ctx, func(tx *sql.Tx) error {
		scanned, err := scanDelete(tx.QueryRowContext(ctx, "SELECT "+deleteColumns+" FROM delete_jobs WHERE job_id = ?1", jobID))
		if err != nil {
			return err
		}
		job = scanned
		return nil
	})
	if err != nil {
		return DeleteJob{}, fmt.Errorf("jobrepo: get delete %s: %w", jobID, err)
	}
	return job, nil
}

func (r *Repository) TransitionDelete(ctx context.Context, jobID string, from, to DeleteState, patch DeletePatch) error {
	if err := ValidateDeleteTransition(from, to); err != nil {
		return err
	}
	return r.updateDelete(ctx, jobID, from, to, patch)
}

func (r *Repository) PatchDelete(ctx context.Context, jobID string, state DeleteState, patch DeletePatch) error {
	return r.updateDelete(ctx, jobID, state, state, patch)
}

func (r *Repository) updateDelete(ctx context.Context, jobID string, from, to DeleteState, patch DeletePatch) error {
	err := r.db.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, transitionDelete, jobID, string(from), string(to),
			patch.DelHeader, patch.DelMsgID, patch.ErrorCode, boolToInt(patch.IncrementAttempts), r.nowMillis())
		if err != nil {
			return err
		}
		changed, err := affectedOne(result)
		if err != nil {
			return err
		}
		if changed {
			return nil
		}
		return staleOrMissing(ctx, tx, "delete_jobs", jobID)
	})
	if err != nil {
		return fmt.Errorf("jobrepo: delete %s from %q to %q: %w", jobID, from, to, err)
	}
	return nil
}

func (r *Repository) ListRecoverableDeletes(ctx context.Context, updatedBefore int64, limit int) ([]DeleteJob, error) {
	var jobs []DeleteJob
	err := r.db.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT "+deleteColumns+` FROM delete_jobs
WHERE state NOT IN ('committed','aborted') AND updated_at <= ?1
ORDER BY updated_at, job_id LIMIT ?2`, updatedBefore, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			job, err := scanDelete(rows)
			if err != nil {
				return err
			}
			jobs = append(jobs, job)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("jobrepo: list recoverable deletes: %w", err)
	}
	return jobs, nil
}

func scanDelete(row rowScanner) (DeleteJob, error) {
	var job DeleteJob
	var origin, state string
	err := row.Scan(&job.JobID, &origin, &job.Scope, &job.Writer, &job.RequestID, &job.BucketChannelID, &job.BucketName,
		&job.ObjectKey, &state, &job.TargetPutMsgID, &job.DelHeader, &job.DelMsgID, &job.ErrorCode, &job.Attempts,
		&job.CreatedAt, &job.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return DeleteJob{}, ErrJobNotFound
	}
	if err != nil {
		return DeleteJob{}, err
	}
	job.Origin, job.State = Origin(origin), DeleteState(state)
	if reason := deleteCorruption(job); reason != "" {
		return DeleteJob{}, fmt.Errorf("%w: delete %s: %s", ErrCorruptRow, job.JobID, reason)
	}
	return job, nil
}

func deleteCorruption(job DeleteJob) string {
	switch {
	case job.Origin != OriginS3 && job.Origin != OriginAdmin:
		return "unknown origin"
	case !validDeleteState(job.State):
		return "unknown state"
	case !validScope(job.Scope):
		return "malformed scope"
	case job.TargetPutMsgID <= 0:
		return "missing target put message"
	}
	return ""
}
