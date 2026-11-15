package remove

import (
	"context"

	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/projection"
	"github.com/PRIEYAN/ButterDB/internal/storage/jobrepo"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

type BucketLookup interface {
	Lookup(ctx context.Context, name bucket.BucketName) (bucket.Info, bool, error)
}

type WriteGates interface {
	AcquireShared(ctx context.Context, bucketChannelID int64) (release func(), err error)
}

type Authority interface {
	RequireWriteAuthority(ctx context.Context, bucketChannelID int64) error
}

type KeyLocks interface {
	Lock(ctx context.Context, bucketChannelID int64, key string) (release func(), err error)
}

type ObjectReader interface {
	Get(ctx context.Context, channelID int64, key string) (object.ObjectMeta, error)
}

type OperationLookup interface {
	LookupOperation(ctx context.Context, channelID int64, opID string, opType wire.OpType) (msgID int64, found bool, err error)
}

type JobStore interface {
	CreateDeleteJob(ctx context.Context, job jobrepo.DeleteJob) error
	DeleteJob(ctx context.Context, jobID string) (jobrepo.DeleteJob, error)
	TransitionDeleteJob(ctx context.Context, jobID string, from, to jobrepo.DeleteState, patch jobrepo.DeletePatch, updatedAt int64) error
}

type ControlSender interface {
	SendControlIdempotent(ctx context.Context, req tgclient.SendControlRequest) (int64, error)
}

type OpProjector interface {
	ProjectFromOp(ctx context.Context, w projection.Writer, obs projection.Observed) (projection.Outcome, error)
}

type PeerResolver interface {
	PeerFor(ctx context.Context, channelID int64) (tgclient.InputPeer, error)
}

type MessageDeleter interface {
	DeleteMessages(ctx context.Context, peer tgclient.InputPeer, msgIDs []int64) error
}

type StableRandomIDFunc func(jobID, purpose string) (int64, error)

type Principal struct {
	Origin    jobrepo.Origin
	Scope     string
	Writer    string
	RequestID string
}

type Principals interface {
	Principal(ctx context.Context) (Principal, error)
}
