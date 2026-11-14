package channelrepo

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

type DeletionSource string

const (
	DeletedExternal  DeletionSource = "external"
	DeletedCleanup   DeletionSource = "cleanup"
	DeletedReconcile DeletionSource = "reconcile"
)

const deletionChunk = 200

type liveObject struct {
	key           string
	storedSize    int64
	plaintextSize int64
}

func (r *Repository) RecordDeletedTx(ctx context.Context, tx Tx, channelID int64, msgIDs []int64, source DeletionSource, recordedAt int64) ([]string, error) {
	if err := validateDeletion(msgIDs, source); err != nil {
		return nil, fmt.Errorf("channelrepo: record deleted %d: %w", channelID, err)
	}
	for _, msgID := range msgIDs {
		_, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO deleted_messages (channel_id, msg_id, source, recorded_at) VALUES (?1, ?2, ?3, ?4)`,
			channelID, msgID, string(source), recordedAt)
		if err != nil {
			return nil, fmt.Errorf("channelrepo: record deleted %d: %w", channelID, err)
		}
	}
	live, err := findLiveObjects(ctx, tx, channelID, msgIDs)
	if err != nil {
		return nil, fmt.Errorf("channelrepo: record deleted %d: %w", channelID, err)
	}
	keys := make([]string, 0, len(live))
	for _, object := range live {
		if err := removeLiveObject(ctx, tx, channelID, object); err != nil {
			return nil, fmt.Errorf("channelrepo: record deleted %d: %w", channelID, err)
		}
		keys = append(keys, object.key)
	}
	return keys, nil
}

func validateDeletion(msgIDs []int64, source DeletionSource) error {
	switch source {
	case DeletedExternal, DeletedCleanup, DeletedReconcile:
	default:
		return fmt.Errorf("%w: deletion source %q", ErrInvalidChannel, source)
	}
	for _, msgID := range msgIDs {
		if msgID <= 0 {
			return fmt.Errorf("%w: message id %d", ErrInvalidChannel, msgID)
		}
	}
	return nil
}

func findLiveObjects(ctx context.Context, tx Tx, channelID int64, msgIDs []int64) ([]liveObject, error) {
	found := map[string]liveObject{}
	for start := 0; start < len(msgIDs); start += deletionChunk {
		chunk := msgIDs[start:min(start+deletionChunk, len(msgIDs))]
		if err := collectLiveObjects(ctx, tx, channelID, chunk, found); err != nil {
			return nil, err
		}
	}
	objects := make([]liveObject, 0, len(found))
	for _, object := range found {
		objects = append(objects, object)
	}
	slices.SortFunc(objects, func(a, b liveObject) int { return strings.Compare(a.key, b.key) })
	return objects, nil
}

func collectLiveObjects(ctx context.Context, tx Tx, channelID int64, chunk []int64, found map[string]liveObject) error {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
	args := make([]any, 0, 1+2*len(chunk))
	args = append(args, channelID)
	for range 2 {
		for _, msgID := range chunk {
			args = append(args, msgID)
		}
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT object_key, stored_size, plaintext_size FROM objects WHERE channel_id = ? AND (put_msg_id IN (`+placeholders+`) OR body_msg_id IN (`+placeholders+`))`,
		args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var object liveObject
		if err := rows.Scan(&object.key, &object.storedSize, &object.plaintextSize); err != nil {
			return err
		}
		found[object.key] = object
	}
	return rows.Err()
}

func removeLiveObject(ctx context.Context, tx Tx, channelID int64, object liveObject) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM objects WHERE channel_id = ?1 AND object_key = ?2`, channelID, object.key); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx,
		`UPDATE buckets SET object_count = object_count - 1, stored_bytes = stored_bytes - ?2, plaintext_bytes = plaintext_bytes - ?3 WHERE channel_id = ?1`,
		channelID, object.storedSize, object.plaintextSize)
	return err
}
