package jobrepo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
)

const multipartDefaultPageSize = 1000

const multipartUploadColumns = `upload_id, bucket_channel_id, bucket_name, object_key, initiator_access_key_id,
  initiated_at, last_activity_at, content_type, user_meta_json, system_meta_json, encrypted,
  checksum_algorithm, checksum_type, state, job_id, etag, put_msg_id, part_list_sha256, completed_at`

const multipartPartColumns = `upload_id, part_number, size, md5, checksum_value, staged_at`

type MultipartUploadQuery struct {
	Prefix         string
	Delimiter      string
	KeyMarker      string
	UploadIDMarker string
	Max            int
}

type MultipartIndex struct {
	db Database
}

type multipartRowScanner interface {
	Scan(dest ...any) error
}

func multipartTransitionAllowed(from, to MultipartState) bool {
	switch from {
	case MultipartOpen:
		return to == MultipartCommitting || to == MultipartAborted
	case MultipartCommitting:
		return to == MultipartOpen || to == MultipartCompleted
	}
	return false
}

func NewMultipartIndex(db Database) (*MultipartIndex, error) {
	if db == nil {
		return nil, fmt.Errorf("jobrepo: %w: database", errs.ErrMissingDependency)
	}
	return &MultipartIndex{db: db}, nil
}

func (m *MultipartIndex) CreateUpload(ctx context.Context, u MultipartUpload) error {
	err := m.db.Write(ctx, func(tx *sql.Tx) error {
		exists, err := multipartUploadExists(ctx, tx, u.UploadID)
		if err != nil {
			return err
		}
		if exists {
			return ErrDuplicateJob
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO multipart_uploads (`+multipartUploadColumns+`)
VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12, ?13, ?14, ?15, ?16, ?17, ?18, ?19)`,
			u.UploadID, u.BucketChannelID, u.BucketName, u.ObjectKey, u.InitiatorAccessKeyID,
			u.InitiatedAt, u.LastActivityAt, u.ContentType, u.UserMetaJSON, u.SystemMetaJSON, multipartBoolInt(u.Encrypted),
			u.ChecksumAlgorithm, u.ChecksumType, string(u.State), u.JobID, u.ETag, u.PutMsgID, u.PartListSHA256, u.CompletedAt)
		return err
	})
	if err != nil {
		return fmt.Errorf("jobrepo: create multipart upload: %w", err)
	}
	return nil
}

func (m *MultipartIndex) GetUpload(ctx context.Context, id string) (MultipartUpload, error) {
	var upload MultipartUpload
	err := m.db.Read(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, `SELECT `+multipartUploadColumns+` FROM multipart_uploads WHERE upload_id = ?1`, id)
		scanned, err := multipartScanUpload(row)
		if errors.Is(err, sql.ErrNoRows) {
			return errs.ErrNoSuchUpload
		}
		upload = scanned
		return err
	})
	if err != nil {
		return MultipartUpload{}, fmt.Errorf("jobrepo: get multipart upload: %w", err)
	}
	return upload, nil
}

func (m *MultipartIndex) PutPart(ctx context.Context, p MultipartPart) error {
	err := m.db.Write(ctx, func(tx *sql.Tx) error {
		state, err := multipartStateOf(ctx, tx, p.UploadID)
		if err != nil {
			return err
		}
		if state == MultipartCommitting {
			return errs.ErrUploadBusy
		}
		if state != MultipartOpen {
			return errs.ErrNoSuchUpload
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO multipart_parts (`+multipartPartColumns+`)
VALUES (?1, ?2, ?3, ?4, ?5, ?6)
ON CONFLICT (upload_id, part_number) DO UPDATE SET
  size = excluded.size, md5 = excluded.md5, checksum_value = excluded.checksum_value, staged_at = excluded.staged_at`,
			p.UploadID, p.PartNumber, p.Size, p.MD5, p.ChecksumValue, p.StagedAt)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE multipart_uploads SET last_activity_at = max(last_activity_at, ?2) WHERE upload_id = ?1`,
			p.UploadID, p.StagedAt)
		return err
	})
	if err != nil {
		return fmt.Errorf("jobrepo: put multipart part %d: %w", p.PartNumber, err)
	}
	return nil
}

func (m *MultipartIndex) ListParts(ctx context.Context, id string, marker int, max int) ([]MultipartPart, bool, error) {
	limit := multipartClampPageSize(max)
	var parts []MultipartPart
	err := m.db.Read(ctx, func(tx *sql.Tx) error {
		exists, err := multipartUploadExists(ctx, tx, id)
		if err != nil {
			return err
		}
		if !exists {
			return errs.ErrNoSuchUpload
		}
		rows, err := tx.QueryContext(ctx, `SELECT `+multipartPartColumns+` FROM multipart_parts
WHERE upload_id = ?1 AND part_number > ?2 ORDER BY part_number LIMIT ?3`, id, marker, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var part MultipartPart
			if err := rows.Scan(&part.UploadID, &part.PartNumber, &part.Size, &part.MD5, &part.ChecksumValue, &part.StagedAt); err != nil {
				return err
			}
			parts = append(parts, part)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, false, fmt.Errorf("jobrepo: list multipart parts: %w", err)
	}
	if len(parts) > limit {
		return parts[:limit], true, nil
	}
	return parts, false, nil
}

func (m *MultipartIndex) ListUploads(ctx context.Context, bucketChannelID int64, q MultipartUploadQuery) ([]MultipartUpload, bool, error) {
	limit := multipartClampPageSize(q.Max)
	var uploads []MultipartUpload
	err := m.db.Read(ctx, func(tx *sql.Tx) error {
		query, args, err := multipartListQuery(ctx, tx, bucketChannelID, q, limit+1)
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			upload, err := multipartScanUpload(rows)
			if err != nil {
				return err
			}
			uploads = append(uploads, upload)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, false, fmt.Errorf("jobrepo: list multipart uploads: %w", err)
	}
	if len(uploads) > limit {
		return uploads[:limit], true, nil
	}
	return uploads, false, nil
}

func (m *MultipartIndex) Transition(ctx context.Context, id string, from, to string, patch MultipartPatch) error {
	fromState, toState := MultipartState(from), MultipartState(to)
	if !multipartTransitionAllowed(fromState, toState) {
		return fmt.Errorf("jobrepo: multipart transition %s to %s: %w", from, to, ErrInvalidTransition)
	}
	err := m.db.Write(ctx, func(tx *sql.Tx) error {
		state, err := multipartStateOf(ctx, tx, id)
		if err != nil {
			return err
		}
		if state != fromState {
			return ErrStaleTransition
		}
		_, err = tx.ExecContext(ctx, `UPDATE multipart_uploads SET
  state = ?2,
  job_id = COALESCE(?3, job_id),
  etag = COALESCE(?4, etag),
  put_msg_id = COALESCE(?5, put_msg_id),
  part_list_sha256 = COALESCE(?6, part_list_sha256),
  completed_at = COALESCE(?7, completed_at),
  last_activity_at = COALESCE(?8, last_activity_at)
WHERE upload_id = ?1`,
			id, to, multipartNullableString(patch.JobID), multipartNullableString(patch.ETag), multipartNullableInt(patch.PutMsgID),
			multipartNullableString(patch.PartListSHA256), multipartNullableInt(patch.CompletedAt), multipartNullableInt(patch.LastActivityAt))
		return err
	})
	if err != nil {
		return fmt.Errorf("jobrepo: transition multipart upload %s to %s: %w", from, to, err)
	}
	return nil
}

func (m *MultipartIndex) Delete(ctx context.Context, id string) error {
	err := m.db.Write(ctx, func(tx *sql.Tx) error {
		exists, err := multipartUploadExists(ctx, tx, id)
		if err != nil {
			return err
		}
		if !exists {
			return errs.ErrNoSuchUpload
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM multipart_parts WHERE upload_id = ?1`, id); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM multipart_uploads WHERE upload_id = ?1`, id)
		return err
	})
	if err != nil {
		return fmt.Errorf("jobrepo: delete multipart upload: %w", err)
	}
	return nil
}

func (m *MultipartIndex) IdleBefore(ctx context.Context, t time.Time, limit int) ([]string, error) {
	var ids []string
	err := m.db.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT upload_id FROM multipart_uploads
WHERE state = 'open' AND last_activity_at < ?1 ORDER BY last_activity_at, upload_id LIMIT ?2`, t.UnixMilli(), multipartIdleLimit(limit))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("jobrepo: list idle multipart uploads: %w", err)
	}
	return ids, nil
}

func multipartListQuery(ctx context.Context, tx *sql.Tx, bucketChannelID int64, q MultipartUploadQuery, limit int) (string, []any, error) {
	var where strings.Builder
	where.WriteString(`bucket_channel_id = ?1 AND state IN ('open','committing')`)
	args := []any{bucketChannelID}
	if q.Prefix != "" {
		args = append(args, q.Prefix)
		where.WriteString(fmt.Sprintf(` AND substr(object_key, 1, length(?%d)) = ?%d`, len(args), len(args)))
	}
	if q.KeyMarker != "" {
		markerClause, markerArgs, err := multipartMarkerClause(ctx, tx, q, len(args))
		if err != nil {
			return "", nil, err
		}
		where.WriteString(markerClause)
		args = append(args, markerArgs...)
	}
	args = append(args, limit)
	query := `SELECT ` + multipartUploadColumns + ` FROM multipart_uploads WHERE ` + where.String() +
		fmt.Sprintf(` ORDER BY object_key, initiated_at, upload_id LIMIT ?%d`, len(args))
	return query, args, nil
}

func multipartMarkerClause(ctx context.Context, tx *sql.Tx, q MultipartUploadQuery, used int) (string, []any, error) {
	if q.UploadIDMarker != "" {
		var initiatedAt int64
		err := tx.QueryRowContext(ctx, `SELECT initiated_at FROM multipart_uploads WHERE upload_id = ?1`, q.UploadIDMarker).Scan(&initiatedAt)
		if err == nil {
			clause := fmt.Sprintf(` AND (object_key, initiated_at, upload_id) > (?%d, ?%d, ?%d)`, used+1, used+2, used+3)
			return clause, []any{q.KeyMarker, initiatedAt, q.UploadIDMarker}, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", nil, err
		}
	}
	return fmt.Sprintf(` AND object_key > ?%d`, used+1), []any{q.KeyMarker}, nil
}

func multipartUploadExists(ctx context.Context, tx *sql.Tx, id string) (bool, error) {
	var found int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM multipart_uploads WHERE upload_id = ?1`, id).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func multipartStateOf(ctx context.Context, tx *sql.Tx, id string) (MultipartState, error) {
	var state string
	err := tx.QueryRowContext(ctx, `SELECT state FROM multipart_uploads WHERE upload_id = ?1`, id).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errs.ErrNoSuchUpload
	}
	if err != nil {
		return "", err
	}
	return MultipartState(state), nil
}

func multipartScanUpload(row multipartRowScanner) (MultipartUpload, error) {
	var upload MultipartUpload
	var encrypted int
	var state string
	err := row.Scan(&upload.UploadID, &upload.BucketChannelID, &upload.BucketName, &upload.ObjectKey, &upload.InitiatorAccessKeyID,
		&upload.InitiatedAt, &upload.LastActivityAt, &upload.ContentType, &upload.UserMetaJSON, &upload.SystemMetaJSON, &encrypted,
		&upload.ChecksumAlgorithm, &upload.ChecksumType, &state, &upload.JobID, &upload.ETag, &upload.PutMsgID, &upload.PartListSHA256, &upload.CompletedAt)
	if err != nil {
		return MultipartUpload{}, err
	}
	upload.Encrypted = encrypted == 1
	upload.State = MultipartState(state)
	if !multipartKnownState(upload.State) {
		return MultipartUpload{}, fmt.Errorf("%w: multipart state %q", ErrCorruptRow, state)
	}
	return upload, nil
}

func multipartKnownState(state MultipartState) bool {
	switch state {
	case MultipartOpen, MultipartCommitting, MultipartCompleted, MultipartAborted:
		return true
	}
	return false
}

func multipartClampPageSize(requested int) int {
	if requested <= 0 || requested > multipartDefaultPageSize {
		return multipartDefaultPageSize
	}
	return requested
}

func multipartBoolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func multipartNullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func multipartNullableInt(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func multipartIdleLimit(requested int) int {
	if requested <= 0 {
		return multipartDefaultPageSize
	}
	return requested
}
