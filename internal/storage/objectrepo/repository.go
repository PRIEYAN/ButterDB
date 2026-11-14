package objectrepo

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
)

type Source interface {
	Read(ctx context.Context, fn func(tx *sql.Tx) error) error
}

type Repository struct {
	db Source
}

func New(db Source) (*Repository, error) {
	if db == nil {
		return nil, fmt.Errorf("objectrepo: %w: database", errs.ErrMissingDependency)
	}
	return &Repository{db: db}, nil
}

const selectObject = `SELECT put_msg_id, body_msg_id, job_id, stored_size, plaintext_size, etag, sha256, content_type,
  user_meta_json, system_meta_json, encrypted, encryption_version, key_id, writer, msg_date
FROM objects WHERE channel_id = ?1 AND object_key = ?2`

func (r *Repository) Get(ctx context.Context, channelID int64, key string) (object.ObjectMeta, error) {
	var meta object.ObjectMeta
	err := r.db.Read(ctx, func(tx *sql.Tx) error {
		found, err := scanObject(tx.QueryRowContext(ctx, selectObject, channelID, key), &meta)
		if err != nil {
			return err
		}
		if !found {
			return errs.ErrNoSuchKey
		}
		return nil
	})
	if err != nil {
		return object.ObjectMeta{}, fmt.Errorf("objectrepo: get %q: %w", key, err)
	}
	meta.ChannelID = channelID
	meta.Key = key
	return meta, nil
}

func scanObject(row *sql.Row, meta *object.ObjectMeta) (bool, error) {
	var userJSON, systemJSON string
	var encrypted int
	err := row.Scan(&meta.PutMsgID, &meta.BodyMsgID, &meta.JobID, &meta.StoredSize, &meta.PlaintextSize,
		&meta.ETag, &meta.SHA256, &meta.ContentType, &userJSON, &systemJSON, &encrypted,
		&meta.EncryptionVersion, &meta.KeyID, &meta.Writer, &meta.MsgDate)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	meta.Encrypted = encrypted == 1
	if meta.UserMeta, err = decodeMeta(userJSON); err != nil {
		return false, fmt.Errorf("user metadata: %w", err)
	}
	if meta.SystemMeta, err = decodeMeta(systemJSON); err != nil {
		return false, fmt.Errorf("system metadata: %w", err)
	}
	return true, nil
}

func decodeMeta(raw string) (map[string]string, error) {
	var values map[string]string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, errors.New("stored metadata is not a json object of strings")
	}
	if len(values) == 0 {
		return nil, nil
	}
	return values, nil
}

func encodeMeta(values map[string]string) (string, error) {
	if len(values) == 0 {
		return "{}", nil
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
