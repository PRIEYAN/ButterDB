package jobrepo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const uploadColumns = `job_id, kind, origin, scope, writer, bucket_channel_id, bucket_name, object_key, multipart_upload_id, state,
  base_put_msg_id, if_match, if_none_match, spool_path, sealed_path, plaintext_size, stored_size, etag, sha256, content_type,
  user_meta_json, system_meta_json, encrypted, encryption_version, key_id, file_salt, nonce_prefix, body_msg_id, put_header,
  put_msg_id, error_code, attempts, created_at, updated_at`

const insertUpload = `INSERT INTO upload_jobs (` + uploadColumns + `)
VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12, ?13, ?14, ?15, ?16, ?17, ?18, ?19, ?20, ?21, ?22, ?23, ?24, ?25, ?26, ?27,
  ?28, ?29, ?30, ?31, ?32, ?33, ?34)`

const transitionUpload = `UPDATE upload_jobs SET
  state = ?3,
  sealed_path = COALESCE(?4, sealed_path),
  body_msg_id = COALESCE(?5, body_msg_id),
  put_header = COALESCE(?6, put_header),
  put_msg_id = COALESCE(?7, put_msg_id),
  error_code = COALESCE(?8, error_code),
  attempts = attempts + ?9,
  updated_at = ?10
WHERE job_id = ?1 AND state = ?2`

func (r *Repository) CreateUpload(ctx context.Context, job UploadJob) error {
	err := r.db.Write(ctx, func(tx *sql.Tx) error {
		return r.insertUploadRow(ctx, tx, job)
	})
	if err != nil {
		return fmt.Errorf("jobrepo: create upload %s: %w", job.JobID, err)
	}
	return nil
}

func (r *Repository) insertUploadRow(ctx context.Context, tx *sql.Tx, job UploadJob) error {
	if job.State != UploadPrepared {
		return fmt.Errorf("new upload must start prepared: %w", ErrInvalidTransition)
	}
	if !validScope(job.Scope) {
		return errors.New("scope must be key:<id> or device:<id>")
	}
	if job.Encrypted && (len(job.FileSalt) != 16 || len(job.NoncePrefix) != 20) {
		return errors.New("encrypted job needs a 16 byte file salt and a 20 byte nonce prefix")
	}
	now := r.nowMillis()
	_, err := tx.ExecContext(ctx, insertUpload,
		job.JobID, string(job.Kind), string(job.Origin), job.Scope, job.Writer, job.BucketChannelID, job.BucketName, job.ObjectKey,
		nullableText(job.MultipartUploadID), string(job.State), job.BasePutMsgID, job.IfMatch, boolToInt(job.IfNoneMatch),
		job.SpoolPath, job.SealedPath, job.PlaintextSize, job.StoredSize, job.ETag, job.SHA256, job.ContentType,
		defaultJSON(job.UserMetaJSON), defaultJSON(job.SystemMetaJSON), boolToInt(job.Encrypted), job.EncryptionVersion,
		job.KeyID, nullableBytes(job.FileSalt), nullableBytes(job.NoncePrefix), job.BodyMsgID, job.PutHeader, job.PutMsgID,
		job.ErrorCode, job.Attempts, now, now)
	if isUniqueViolation(err) {
		return ErrDuplicateJob
	}
	return err
}

func defaultJSON(raw string) string {
	if raw == "" {
		return "{}"
	}
	return raw
}

func (r *Repository) GetUpload(ctx context.Context, jobID string) (UploadJob, error) {
	var job UploadJob
	err := r.db.Read(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, "SELECT "+uploadColumns+" FROM upload_jobs WHERE job_id = ?1", jobID)
		scanned, err := scanUpload(row)
		if err != nil {
			return err
		}
		job = scanned
		return nil
	})
	if err != nil {
		return UploadJob{}, fmt.Errorf("jobrepo: get upload %s: %w", jobID, err)
	}
	return job, nil
}

func (r *Repository) TransitionUpload(ctx context.Context, jobID string, from, to UploadState, patch UploadPatch) error {
	if err := ValidateUploadTransition(from, to); err != nil {
		return err
	}
	return r.updateUpload(ctx, jobID, from, to, patch)
}

func (r *Repository) PatchUpload(ctx context.Context, jobID string, state UploadState, patch UploadPatch) error {
	return r.updateUpload(ctx, jobID, state, state, patch)
}

func (r *Repository) updateUpload(ctx context.Context, jobID string, from, to UploadState, patch UploadPatch) error {
	err := r.db.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, transitionUpload, jobID, string(from), string(to),
			patch.SealedPath, patch.BodyMsgID, patch.PutHeader, patch.PutMsgID, patch.ErrorCode,
			boolToInt(patch.IncrementAttempts), r.nowMillis())
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
		return staleOrMissing(ctx, tx, "upload_jobs", jobID)
	})
	if err != nil {
		return fmt.Errorf("jobrepo: upload %s from %q to %q: %w", jobID, from, to, err)
	}
	return nil
}

func (r *Repository) ListRecoverableUploads(ctx context.Context, updatedBefore int64, limit int) ([]UploadJob, error) {
	var jobs []UploadJob
	err := r.db.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT "+uploadColumns+` FROM upload_jobs
WHERE state NOT IN ('committed','aborted') AND updated_at <= ?1
ORDER BY updated_at, job_id LIMIT ?2`, updatedBefore, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			job, err := scanUpload(rows)
			if err != nil {
				return err
			}
			jobs = append(jobs, job)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("jobrepo: list recoverable uploads: %w", err)
	}
	return jobs, nil
}

func (r *Repository) OpenUploadJobIDs(ctx context.Context, jobIDs []string) (map[string]struct{}, error) {
	open := make(map[string]struct{})
	if len(jobIDs) == 0 {
		return open, nil
	}
	err := r.db.Read(ctx, func(tx *sql.Tx) error {
		for start := 0; start < len(jobIDs); start += openJobBatchSize {
			batch := jobIDs[start:min(start+openJobBatchSize, len(jobIDs))]
			if err := collectOpenJobs(ctx, tx, batch, open); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("jobrepo: open upload jobs: %w", err)
	}
	return open, nil
}

const openJobBatchSize = 200

func collectOpenJobs(ctx context.Context, tx *sql.Tx, batch []string, open map[string]struct{}) error {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
	args := make([]any, len(batch))
	for i, id := range batch {
		args[i] = id
	}
	rows, err := tx.QueryContext(ctx, "SELECT job_id FROM upload_jobs WHERE state NOT IN ('committed','aborted') AND job_id IN ("+placeholders+")", args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		open[id] = struct{}{}
	}
	return rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanUpload(row rowScanner) (UploadJob, error) {
	var job UploadJob
	var kind, origin, state string
	var multipartID sql.NullString
	var ifNoneMatch, encrypted int
	err := row.Scan(&job.JobID, &kind, &origin, &job.Scope, &job.Writer, &job.BucketChannelID, &job.BucketName, &job.ObjectKey,
		&multipartID, &state, &job.BasePutMsgID, &job.IfMatch, &ifNoneMatch, &job.SpoolPath, &job.SealedPath, &job.PlaintextSize,
		&job.StoredSize, &job.ETag, &job.SHA256, &job.ContentType, &job.UserMetaJSON, &job.SystemMetaJSON, &encrypted,
		&job.EncryptionVersion, &job.KeyID, &job.FileSalt, &job.NoncePrefix, &job.BodyMsgID, &job.PutHeader, &job.PutMsgID,
		&job.ErrorCode, &job.Attempts, &job.CreatedAt, &job.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return UploadJob{}, ErrJobNotFound
	}
	if err != nil {
		return UploadJob{}, err
	}
	job.Kind, job.Origin, job.State = UploadKind(kind), Origin(origin), UploadState(state)
	job.MultipartUploadID = multipartID.String
	job.IfNoneMatch, job.Encrypted = ifNoneMatch == 1, encrypted == 1
	if reason := uploadCorruption(job, ifNoneMatch, encrypted, multipartID.Valid); reason != "" {
		return UploadJob{}, fmt.Errorf("%w: upload %s: %s", ErrCorruptRow, job.JobID, reason)
	}
	return job, nil
}

func uploadCorruption(job UploadJob, ifNoneMatch, encrypted int, hasMultipartID bool) string {
	switch {
	case job.Kind != KindPut && job.Kind != KindCopy && job.Kind != KindMultipart:
		return "unknown kind"
	case job.Origin != OriginS3 && job.Origin != OriginAdmin:
		return "unknown origin"
	case !validUploadState(job.State):
		return "unknown state"
	case !validScope(job.Scope):
		return "malformed scope"
	case ifNoneMatch != 0 && ifNoneMatch != 1, encrypted != 0 && encrypted != 1:
		return "malformed flag"
	case (job.Kind == KindMultipart) != hasMultipartID:
		return "multipart id does not match kind"
	case job.Encrypted && (len(job.FileSalt) != 16 || len(job.NoncePrefix) != 20):
		return "encrypted job without salt and nonce prefix"
	case !job.Encrypted && (len(job.FileSalt) != 0 || len(job.NoncePrefix) != 0):
		return "unencrypted job carries salt or nonce prefix"
	}
	return ""
}
