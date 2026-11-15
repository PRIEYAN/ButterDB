package buckets

import (
	"context"
	"io"
	"log/slog"

	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/projection"
	"github.com/PRIEYAN/ButterDB/internal/syncer"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

type BucketReader interface {
	Lookup(ctx context.Context, name bucket.BucketName) (bucket.Info, bool, error)
	LookupChannel(ctx context.Context, channelID int64) (bucket.Info, bool, error)
	ListLive(ctx context.Context) ([]bucket.Info, error)
}

type ChannelRecords interface {
	Channel(ctx context.Context, channelID int64) (syncer.ChannelState, bool, error)
	Channels(ctx context.Context) ([]syncer.ChannelState, error)
	UpsertChannel(ctx context.Context, state syncer.ChannelState) error
	UpdateAccessHash(ctx context.Context, channelID, accessHash int64) error
}

type EmptyChannelSyncer interface {
	InitialSyncEmptyChannel(ctx context.Context, channelID int64) error
}

type ChannelGate interface {
	Acquire(ctx context.Context, channelID int64) (release func(), err error)
}

type ChannelCreator interface {
	CreateChannel(ctx context.Context, title, about string) (tgclient.InputPeer, error)
}

type PeerResolver interface {
	ResolveChannel(ctx context.Context, channelID int64) (tgclient.InputPeer, error)
}

type MessageReader interface {
	GetMessages(ctx context.Context, peer tgclient.InputPeer, msgIDs []int64) ([]tgclient.HistoryMessage, error)
}

type ControlSender interface {
	SendControlIdempotent(ctx context.Context, req tgclient.SendControlRequest) (int64, error)
}

type OpProjector interface {
	ProjectFromOp(ctx context.Context, w projection.Writer, obs projection.Observed) (projection.Outcome, error)
}

type StableRandomIDFunc func(jobID, purpose string) (int64, error)

type KeyMinter interface {
	MintEncryptionConfig(ctx context.Context, bucketChannelID int64) (wire.Op, error)
}

type KeyPresence interface {
	HasActiveKey(ctx context.Context, bucketChannelID int64) (bool, error)
}

type ActiveJobs interface {
	HasActiveJobs(ctx context.Context, bucketChannelID int64) (bool, error)
}

type BucketWriteGate interface {
	AcquireExclusive(ctx context.Context, bucketChannelID int64) (release func(), err error)
}

type RegistryState struct {
	ChannelID      int64
	AccessHash     int64
	MarkerMsgID    int64
	PrimaryUserID  int64
	KeyFingerprint string
	DeploymentID   string
}

type RegistryStateStore interface {
	LoadRegistryState(ctx context.Context) (RegistryState, bool, error)
	SaveRegistryState(ctx context.Context, state RegistryState) error
}

type PublishDependencies struct {
	Channels       ChannelRecords
	Resolver       PeerResolver
	Messages       MessageReader
	Sender         ControlSender
	Projector      OpProjector
	Writer         projection.Writer
	StableRandomID StableRandomIDFunc
	Clock          clock.Clock
	Logger         *slog.Logger
	ActorUserID    int64
}

type Dependencies struct {
	Publish      PublishDependencies
	Buckets      BucketReader
	Scanner      EmptyChannelSyncer
	Gate         ChannelGate
	Creator      ChannelCreator
	Keys         KeyMinter
	KeyPresence  KeyPresence
	Jobs         ActiveJobs
	WriteGates   BucketWriteGate
	Random       io.Reader
	DeploymentID string
}

type RegistryDependencies struct {
	Publish        PublishDependencies
	State          RegistryStateStore
	Scanner        EmptyChannelSyncer
	Gate           ChannelGate
	Creator        ChannelCreator
	DeploymentID   string
	KeyFingerprint string
}
