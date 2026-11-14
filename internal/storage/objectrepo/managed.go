package objectrepo

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

const managedMessageBatchSize = 200

var managedSources = []struct{ table, column, guard string }{
	{"objects", "put_msg_id", ""},
	{"objects", "body_msg_id", "body_msg_id > 0 AND "},
	{"object_bodies", "msg_id", ""},
	{"replay_log", "msg_id", ""},
	{"pending_message_cleanup", "msg_id", ""},
}

func (r *Repository) ManagedMsgIDsIn(ctx context.Context, channelID int64, candidates []int64) (map[int64]struct{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("objectrepo: read managed messages: %w", err)
	}
	if channelID <= 0 {
		return nil, fmt.Errorf("objectrepo: read managed messages: channel id required")
	}
	owned := make(map[int64]struct{})
	if len(candidates) == 0 {
		return owned, nil
	}
	err := r.db.Read(ctx, func(tx *sql.Tx) error {
		for start := 0; start < len(candidates); start += managedMessageBatchSize {
			if err := ctx.Err(); err != nil {
				return err
			}
			batch := candidates[start:min(start+managedMessageBatchSize, len(candidates))]
			if err := managedBatch(ctx, tx, channelID, batch, owned); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("objectrepo: read managed messages: %w", err)
	}
	return owned, nil
}

func managedQuery(batchSize int) string {
	placeholders := make([]string, batchSize)
	for i := range placeholders {
		placeholders[i] = fmt.Sprintf("?%d", i+2)
	}
	ids := strings.Join(placeholders, ",")
	selects := make([]string, len(managedSources))
	for i, source := range managedSources {
		selects[i] = fmt.Sprintf("SELECT %[2]s FROM %[1]s WHERE channel_id = ?1 AND %[3]s%[2]s IN (%[4]s)", source.table, source.column, source.guard, ids)
	}
	return strings.Join(selects, " UNION ")
}

func managedBatch(ctx context.Context, q querier, channelID int64, batch []int64, owned map[int64]struct{}) error {
	args := make([]any, 0, len(batch)+1)
	args = append(args, channelID)
	for _, id := range batch {
		args = append(args, id)
	}
	rows, err := q.QueryContext(ctx, managedQuery(len(batch)), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		owned[id] = struct{}{}
	}
	return rows.Err()
}
