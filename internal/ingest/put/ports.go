package put

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/ingest"
	"github.com/PRIEYAN/ButterDB/internal/ingest/admission"
	"github.com/PRIEYAN/ButterDB/internal/ingest/spool"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/projection"
	"github.com/PRIEYAN/ButterDB/internal/storage/jobrepo"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/flow"
)

var (
	ErrInvalidRequest       = errors.New("put: invalid request")
	ErrIdempotencyConflict  = errors.New("put: idempotency key reused with a different request")
	ErrControlProjection    = errors.New("put: control message accepted but projection failed")
	ErrJobAborted           = errors.New("put: job was aborted")
	ErrCommitOutcomeUnknown = errors.New("put: commit outcome unknown")
	ErrProjectionRejected   = errors.New("put: projection rejected the op")
)

type ControlProjectionError struct {
	MsgID int64
	Err   error
}

func (e *ControlProjectionError) Error() string {
	return fmt.Sprintf("put: control message %d accepted but projection failed: %v", e.MsgID, e.Err)
}

func (e *ControlProjectionError) Unwrap() []error {
	return []error{ErrControlProjection, e.Err}
}

type OutcomeUnknownError struct {
	JobID string
	Err   error
}

func (e *OutcomeUnknownError) Error() string {
	return fmt.Sprintf("put: job %s outcome unknown: %v", e.JobID, e.Err)
}

func (e *OutcomeUnknownError) Unwrap() []error {
	return []error{ErrCommitOutcomeUnknown, e.Err}
}

type SpoolStore interface {
	Write(ctx context.Context, jobID string, body io.Reader, declared int64, maxStored int64) (spool.File, error)
	Open(f spool.File) (io.ReadSeekCloser, error)
	Remove(jobID string) error
}

type Admission interface {
	AcquirePut(ctx context.Context, b admission.Budget, scope string) (release func(), err error)
	WaitSend(ctx context.Context, b admission.Budget, class flow.SendClass) error
	WaitFlood(ctx context.Context, b admission.Budget, wait time.Duration) error
}

type JobStore interface {
	CreateJob(ctx context.Context, job jobrepo.UploadJob, idempotency *jobrepo.IdempotencyRecord) error
	GetJob(ctx context.Context, jobID string) (jobrepo.UploadJob, error)
	TransitionJob(ctx context.Context, jobID string, from, to jobrepo.UploadState, patch jobrepo.UploadPatch) error
	LookupIdempotency(ctx context.Context, scope, idempotencyKey string) (jobrepo.IdempotencyRecord, bool, error)
}

type BucketLookup interface {
	Lookup(ctx context.Context, name bucket.BucketName) (bucket.Info, bool, error)
}

type BucketAuthority interface {
	RequireWriteAuthority(ctx context.Context, bucketChannelID int64) error
	EnsureKey(ctx context.Context, bucketChannelID int64) error
}

type WriteGate interface {
	AcquireShared(ctx context.Context, bucketChannelID int64) (func(), error)
}

type ObjectReader interface {
	Get(ctx context.Context, channelID int64, key string) (object.ObjectMeta, error)
}

type EncryptionKey struct {
	KeyID      string
	WrappedDEK []byte
	KeyCheck   []byte
}

type EncryptionKeys interface {
	ActiveKey(ctx context.Context, channelID int64, kekFingerprint string) (EncryptionKey, bool, error)
	Key(ctx context.Context, channelID int64, keyID string) (EncryptionKey, bool, error)
}

type DEK interface {
	Bytes() []byte
	Clear()
}

type DEKUnwrapper interface {
	UnwrapDEK(wrap []byte, check []byte, bucketChannelID int64, kid string) (DEK, error)
}

type PeerResolver interface {
	ResolveChannel(ctx context.Context, channelID int64) (tgclient.InputPeer, error)
}

type BodySender interface {
	SendFileIdempotent(ctx context.Context, req tgclient.SendFileRequest) (tgclient.SendFileResult, error)
}

type ControlSender interface {
	SendControlIdempotent(ctx context.Context, req tgclient.SendControlRequest) (int64, error)
}

type MessageReader interface {
	GetMessages(ctx context.Context, peer tgclient.InputPeer, msgIDs []int64) ([]tgclient.HistoryMessage, error)
}

type MessageDeleter interface {
	DeleteMessages(ctx context.Context, peer tgclient.InputPeer, msgIDs []int64) error
}

type OpProjector interface {
	ProjectFromOp(ctx context.Context, w projection.Writer, obs projection.Observed) (projection.Outcome, error)
}

type StableRandomIDFunc func(jobID, purpose string) (int64, error)

type Config struct {
	RequestBudget  time.Duration
	KEKFingerprint string
	ActorUserID    int64
}

type KeyLocks interface {
	Lock(ctx context.Context, bucketChannelID int64, key string) (release func(), err error)
}

type Dependencies struct {
	Spool          SpoolStore
	Admission      Admission
	Jobs           JobStore
	Buckets        BucketLookup
	Authority      BucketAuthority
	Gates          WriteGate
	Objects        ObjectReader
	Keys           EncryptionKeys
	Unwrap         DEKUnwrapper
	Peers          PeerResolver
	Bodies         BodySender
	Controls       ControlSender
	Deleter        MessageDeleter
	Messages       MessageReader
	Projector      OpProjector
	Writer         projection.Writer
	Cleanup        ingest.CleanupDrainer
	StableRandomID StableRandomIDFunc
	KeyLocks       KeyLocks
	Clock          clock.Clock
	Random         io.Reader
	Logger         *slog.Logger
	Config         Config
}

func (d Dependencies) validate() error {
	missing := func(name string) error {
		return fmt.Errorf("put: %w: %s", errs.ErrMissingDependency, name)
	}
	switch {
	case d.Spool == nil:
		return missing("spool")
	case d.Admission == nil:
		return missing("admission")
	case d.Jobs == nil:
		return missing("jobs")
	case d.Buckets == nil:
		return missing("buckets")
	case d.Authority == nil:
		return missing("authority")
	case d.Gates == nil:
		return missing("gates")
	case d.Objects == nil:
		return missing("objects")
	case d.Keys == nil:
		return missing("keys")
	case d.Unwrap == nil:
		return missing("unwrap")
	case d.Peers == nil:
		return missing("peers")
	case d.Bodies == nil:
		return missing("bodies")
	case d.Controls == nil:
		return missing("controls")
	case d.Deleter == nil:
		return missing("deleter")
	case d.Messages == nil:
		return missing("messages")
	case d.Projector == nil:
		return missing("projector")
	case d.Writer == nil:
		return missing("writer")
	case d.Cleanup == nil:
		return missing("cleanup")
	case d.StableRandomID == nil:
		return missing("stable random id")
	case d.KeyLocks == nil:
		return missing("key locks")
	case d.Clock == nil:
		return missing("clock")
	case d.Random == nil:
		return missing("random")
	case d.Logger == nil:
		return missing("logger")
	case d.Config.RequestBudget <= 0:
		return missing("request budget")
	case d.Config.KEKFingerprint == "":
		return missing("kek fingerprint")
	case d.Config.ActorUserID <= 0:
		return missing("actor user id")
	}
	return nil
}
