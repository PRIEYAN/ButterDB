package ingest

import "context"

type CleanupQueue interface {
	DueCleanupChannels(ctx context.Context, now int64, limit int) ([]int64, error)
	ClaimCleanupBatch(ctx context.Context, channelID, now int64, limit int) ([]int64, error)
	ConfirmCleanupDeleted(ctx context.Context, channelID int64, msgIDs []int64, recordedAt int64) error
	DeferCleanup(ctx context.Context, channelID int64, msgIDs []int64, nextAttemptAt int64, lastError string) error
}

type CleanupDrainer interface {
	DrainChannel(ctx context.Context, channelID int64) error
}
