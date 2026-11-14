package log

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/PRIEYAN/ButterDB/internal/projection"
)

var ErrChannelNotEmpty = errors.New("projection log: channel not empty")

func occupancyQueries() []string {
	return []string{
		`SELECT 1 FROM replay_log WHERE channel_id = ? LIMIT 1`,
		`SELECT 1 FROM replay_log_rejects WHERE channel_id = ? LIMIT 1`,
		`SELECT 1 FROM buckets WHERE channel_id = ? LIMIT 1`,
		`SELECT 1 FROM objects WHERE channel_id = ? LIMIT 1`,
		`SELECT 1 FROM object_tombstones WHERE channel_id = ? LIMIT 1`,
		`SELECT 1 FROM object_bodies WHERE channel_id = ? LIMIT 1`,
	}
}

func ChannelIsEmpty(ctx context.Context, tx projection.Tx, channelID int64) (bool, error) {
	for _, query := range occupancyQueries() {
		var found int
		err := tx.QueryRowContext(ctx, query, channelID).Scan(&found)
		if err == nil {
			return false, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return false, fmt.Errorf("projection log: check channel empty: %w", err)
		}
	}
	return true, nil
}
