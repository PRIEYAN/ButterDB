package sweeper

import (
	"context"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/storage/jobrepo"
)

type JobStore interface {
	ListRecoverableUploads(ctx context.Context, updatedBefore int64, limit int) ([]jobrepo.UploadJob, error)
	ListRecoverableDeletes(ctx context.Context, updatedBefore int64, limit int) ([]jobrepo.DeleteJob, error)
	OpenUploadJobIDs(ctx context.Context, jobIDs []string) (map[string]struct{}, error)
	DeleteExpiredIdempotency(ctx context.Context, now int64) (int, error)
	PruneTerminalJobs(ctx context.Context, updatedBefore int64) (int, error)
}

type UploadResumer interface {
	Resume(ctx context.Context, jobID string) error
}

type DeleteResumer interface {
	Resume(ctx context.Context, jobID string) error
}

type DueCleanup interface {
	DueCleanupChannels(ctx context.Context, now int64, limit int) ([]int64, error)
}

type OrphanBodies interface {
	BodyChannels(ctx context.Context) ([]int64, error)
	OrphanBodies(ctx context.Context, channelID, olderThanUnix, afterMsgID int64, limit int) ([]int64, []string, error)
	QueueOrphanBodies(ctx context.Context, channelID int64, msgIDs []int64) (int, error)
}

type Sleeper interface {
	Sleep(ctx context.Context, d time.Duration) error
}
