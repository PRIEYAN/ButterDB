package syncer

import (
	"context"
	"errors"
	"fmt"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/projection"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

type ChannelKind string

const (
	KindRegistry ChannelKind = "registry"
	KindBucket   ChannelKind = "bucket"
)

type ChannelState struct {
	ChannelID              int64
	AccessHash             int64
	Kind                   ChannelKind
	Title                  string
	Deleted                bool
	LastSyncedMsg          int64
	Pts                    int
	InitialSyncDone        bool
	NeedsProjectionRebuild bool
	LastReconciledAt       int64
}

type DeletionSource string

const (
	DeletedExternal  DeletionSource = "external"
	DeletedCleanup   DeletionSource = "cleanup"
	DeletedReconcile DeletionSource = "reconcile"
)

type LiveObjectRef struct {
	Key           string
	PutMsgID      int64
	BackingMsgIDs []int64
}

var ErrNoAuthority = fmt.Errorf("syncer: channel has no authority: %w", errs.ErrSlowDown)
var ErrUnknownChannel = errors.New("syncer: unknown channel")
var ErrHistoryNoProgress = errors.New("syncer: history scan made no progress")
var ErrChannelNotEmpty = errors.New("syncer: channel already has projected history")

type ChannelGate interface {
	Acquire(ctx context.Context, channelID int64) (release func(), err error)
}

type ChannelStore interface {
	Channel(ctx context.Context, channelID int64) (ChannelState, bool, error)
	Channels(ctx context.Context) ([]ChannelState, error)
	UpsertChannel(ctx context.Context, state ChannelState) error
	UpdateAccessHash(ctx context.Context, channelID, accessHash int64) error
}

type SyncCursorStore interface {
	SetSyncCursorTx(ctx context.Context, tx projection.Tx, channelID, lastSyncedMsg int64, pts int) error
	MarkInitialSyncDoneTx(ctx context.Context, tx projection.Tx, channelID, lastSyncedMsg int64, pts int) error
	SetNeedsProjectionRebuild(ctx context.Context, channelID int64, needed bool) error
	SetLastReconciledAt(ctx context.Context, channelID, at int64) error
}

type ScanSpool interface {
	ClearSpool(ctx context.Context, channelID int64) error
	SpoolPage(ctx context.Context, channelID int64, msgs []tgclient.HistoryMessage) error
	SpooledPageTx(ctx context.Context, tx projection.Tx, channelID, afterMsgID int64, limit int) ([]tgclient.HistoryMessage, error)
}

type DeletionStore interface {
	RecordDeletedTx(ctx context.Context, tx projection.Tx, channelID int64, msgIDs []int64, source DeletionSource, recordedAt int64) (removedKeys []string, err error)
}

type LiveObjects interface {
	LiveMessageIDs(ctx context.Context, channelID int64) ([]LiveObjectRef, error)
}

type OpIngester interface {
	IngestTx(ctx context.Context, tx projection.Tx, obs projection.Observed) (projection.Outcome, error)
}

type HistoryScanner interface {
	EnsureAuthoritative(ctx context.Context, channelID int64) error
	FullScan(ctx context.Context, channelID int64) error
	InitialSyncEmptyChannel(ctx context.Context, channelID int64) error
}
