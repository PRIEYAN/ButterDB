package remove

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/ingest"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/projection"
	"github.com/PRIEYAN/ButterDB/internal/storage/jobrepo"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

const delPurpose = "del"

var _ Service = (*Deleter)(nil)

type Dependencies struct {
	Buckets        BucketLookup
	Gates          WriteGates
	Authority      Authority
	KeyLocks       KeyLocks
	Objects        ObjectReader
	Operations     OperationLookup
	Jobs           JobStore
	Sender         ControlSender
	Peers          PeerResolver
	Messages       MessageReader
	Projector      OpProjector
	Writer         projection.Writer
	Drainer        ingest.CleanupDrainer
	Principals     Principals
	StableRandomID StableRandomIDFunc
	Clock          clock.Clock
	Random         io.Reader
	Logger         *slog.Logger
	ActorUserID    int64
}

type Deleter struct {
	deps Dependencies
}

func New(deps Dependencies) (*Deleter, error) {
	missing := ""
	switch {
	case deps.Buckets == nil:
		missing = "buckets"
	case deps.Gates == nil:
		missing = "write gates"
	case deps.Authority == nil:
		missing = "authority"
	case deps.KeyLocks == nil:
		missing = "key locks"
	case deps.Objects == nil:
		missing = "objects"
	case deps.Operations == nil:
		missing = "operations"
	case deps.Jobs == nil:
		missing = "jobs"
	case deps.Sender == nil:
		missing = "sender"
	case deps.Peers == nil:
		missing = "peers"
	case deps.Messages == nil:
		missing = "messages"
	case deps.Projector == nil:
		missing = "projector"
	case deps.Writer == nil:
		missing = "projection writer"
	case deps.Drainer == nil:
		missing = "drainer"
	case deps.Principals == nil:
		missing = "principals"
	case deps.StableRandomID == nil:
		missing = "stable random id"
	case deps.Clock == nil:
		missing = "clock"
	case deps.Random == nil:
		missing = "random"
	case deps.Logger == nil:
		missing = "logger"
	case deps.ActorUserID <= 0:
		missing = "actor user id"
	}
	if missing != "" {
		return nil, fmt.Errorf("remove: %w: %s", errs.ErrMissingDependency, missing)
	}
	return &Deleter{deps: deps}, nil
}

func (s *Deleter) Delete(ctx context.Context, name bucket.BucketName, key object.Key) error {
	if err := key.Validate(); err != nil {
		return fmt.Errorf("remove: %w", err)
	}
	principal, err := s.deps.Principals.Principal(ctx)
	if err != nil {
		return fmt.Errorf("remove: resolve principal: %w", err)
	}
	info, found, err := s.deps.Buckets.Lookup(ctx, name)
	if err != nil {
		return fmt.Errorf("remove: look up bucket: %w", err)
	}
	if !found {
		return fmt.Errorf("remove: %w", errs.ErrNoSuchBucket)
	}
	release, err := s.deps.Gates.AcquireShared(ctx, info.ChannelID)
	if err != nil {
		return fmt.Errorf("remove: %w", err)
	}
	defer release()
	if err := s.deps.Authority.RequireWriteAuthority(ctx, info.ChannelID); err != nil {
		return fmt.Errorf("remove: %w", err)
	}
	job, unlock, err := s.prepare(ctx, info, key, principal)
	if err != nil || job == nil {
		return err
	}
	defer unlock()
	if err := s.send(ctx, job); err != nil {
		return err
	}
	unlock()
	s.drain(ctx, info.ChannelID)
	return nil
}

func (s *Deleter) Resume(ctx context.Context, jobID string) error {
	job, err := s.deps.Jobs.DeleteJob(ctx, jobID)
	if err != nil {
		return fmt.Errorf("remove: load delete job: %w", err)
	}
	if job.State == jobrepo.DeleteCommitted || job.State == jobrepo.DeleteAborted {
		return nil
	}
	release, err := s.deps.Gates.AcquireShared(ctx, job.BucketChannelID)
	if err != nil {
		return fmt.Errorf("remove: %w", err)
	}
	defer release()
	if err := s.deps.Authority.RequireWriteAuthority(ctx, job.BucketChannelID); err != nil {
		return fmt.Errorf("remove: %w", err)
	}
	unlock, err := s.lockKey(ctx, job.BucketChannelID, job.ObjectKey)
	if err != nil {
		return err
	}
	defer unlock()
	done, err := s.resume(ctx, &job)
	if err != nil || !done {
		return err
	}
	unlock()
	s.drain(ctx, job.BucketChannelID)
	return nil
}

func (s *Deleter) prepare(ctx context.Context, info bucket.Info, key object.Key, principal Principal) (*jobrepo.DeleteJob, func(), error) {
	unlock, err := s.lockKey(ctx, info.ChannelID, string(key))
	if err != nil {
		return nil, nil, err
	}
	live, err := s.deps.Objects.Get(ctx, info.ChannelID, string(key))
	if errors.Is(err, errs.ErrNoSuchKey) {
		unlock()
		return nil, nil, nil
	}
	if err != nil {
		unlock()
		return nil, nil, fmt.Errorf("remove: load live object: %w", err)
	}
	jobID, err := newJobID(s.deps.Random)
	if err != nil {
		unlock()
		return nil, nil, err
	}
	now := s.deps.Clock.Now().UnixMilli()
	header, err := formatDel(jobID, string(key), live.PutMsgID, principal.Writer, s.deps.Clock.Now().Unix())
	if err != nil {
		unlock()
		return nil, nil, err
	}
	job := jobrepo.DeleteJob{
		JobID:           jobID,
		Origin:          principal.Origin,
		Scope:           principal.Scope,
		Writer:          principal.Writer,
		RequestID:       principal.RequestID,
		BucketChannelID: info.ChannelID,
		BucketName:      string(info.Name),
		ObjectKey:       string(key),
		State:           jobrepo.DeletePrepared,
		TargetPutMsgID:  live.PutMsgID,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := s.deps.Jobs.CreateDeleteJob(ctx, job); err != nil {
		unlock()
		return nil, nil, fmt.Errorf("remove: persist delete job: %w", err)
	}
	if err := s.advance(ctx, &job, jobrepo.DeleteDelAttempted, jobrepo.DeletePatch{DelHeader: &header}); err != nil {
		unlock()
		return nil, nil, err
	}
	return &job, unlock, nil
}

func (s *Deleter) lockKey(ctx context.Context, channelID int64, key string) (func(), error) {
	release, err := s.deps.KeyLocks.Lock(ctx, channelID, key)
	if err != nil {
		return nil, fmt.Errorf("remove: lock key: %w", err)
	}
	var once sync.Once
	return func() { once.Do(release) }, nil
}

func (s *Deleter) resume(ctx context.Context, job *jobrepo.DeleteJob) (bool, error) {
	if job.State != jobrepo.DeletePrepared {
		msgID, found, err := s.deps.Operations.LookupOperation(ctx, job.BucketChannelID, job.JobID, wire.OpDel)
		if err != nil {
			return false, fmt.Errorf("remove: look up projected tombstone: %w", err)
		}
		if found {
			return true, s.commit(ctx, job, msgID)
		}
	}
	live, err := s.deps.Objects.Get(ctx, job.BucketChannelID, job.ObjectKey)
	if err != nil && !errors.Is(err, errs.ErrNoSuchKey) {
		return false, fmt.Errorf("remove: load live object: %w", err)
	}
	if err != nil || live.PutMsgID != job.TargetPutMsgID {
		return false, s.abort(ctx, job)
	}
	switch job.State {
	case jobrepo.DeletePrepared:
		header, err := formatDel(job.JobID, job.ObjectKey, job.TargetPutMsgID, job.Writer, s.deps.Clock.Now().Unix())
		if err != nil {
			return false, err
		}
		if err := s.advance(ctx, job, jobrepo.DeleteDelAttempted, jobrepo.DeletePatch{DelHeader: &header}); err != nil {
			return false, err
		}
	case jobrepo.DeleteUncertainDel:
		if err := s.advance(ctx, job, jobrepo.DeleteDelAttempted, jobrepo.DeletePatch{IncrementAttempts: true}); err != nil {
			return false, err
		}
	}
	if err := s.send(ctx, job); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Deleter) send(ctx context.Context, job *jobrepo.DeleteJob) error {
	randomID, err := s.deps.StableRandomID(job.JobID, delPurpose)
	if err != nil {
		return fmt.Errorf("remove: random id for tombstone: %w", err)
	}
	peer, err := s.deps.Peers.PeerFor(ctx, job.BucketChannelID)
	if err != nil {
		return fmt.Errorf("remove: resolve channel %d: %w", job.BucketChannelID, err)
	}
	msgID, sendErr := s.deps.Sender.SendControlIdempotent(ctx, tgclient.SendControlRequest{Peer: peer, Text: job.DelHeader, RandomID: randomID})
	switch {
	case sendErr == nil && msgID > 0:
	case sendErr == nil:
		return s.markUncertain(ctx, job, fmt.Errorf("no message id returned"))
	case msgID > 0:
		s.deps.Logger.Warn("remove: tombstone send reported an error after the message was accepted", "channel_id", job.BucketChannelID, "msg_id", msgID)
	case errors.Is(sendErr, tgclient.ErrSendOutcomeUnknown):
		return s.markUncertain(ctx, job, sendErr)
	case errors.Is(sendErr, tgclient.ErrFloodWait):
		return fmt.Errorf("remove: tombstone send for channel %d: %w: %w", job.BucketChannelID, errs.ErrSlowDown, sendErr)
	default:
		return fmt.Errorf("remove: tombstone send for channel %d: %w", job.BucketChannelID, sendErr)
	}
	return s.project(ctx, job, msgID)
}

func (s *Deleter) markUncertain(ctx context.Context, job *jobrepo.DeleteJob, cause error) error {
	code := "outcome_unknown"
	if err := s.advance(ctx, job, jobrepo.DeleteUncertainDel, jobrepo.DeletePatch{ErrorCode: &code}); err != nil {
		return err
	}
	return fmt.Errorf("remove: tombstone outcome unknown for channel %d: %w: %w", job.BucketChannelID, errs.ErrSlowDown, cause)
}

func (s *Deleter) project(ctx context.Context, job *jobrepo.DeleteJob, msgID int64) error {
	sent, err := s.readBack(ctx, job, msgID)
	if err != nil {
		return err
	}
	outcome, err := s.deps.Projector.ProjectFromOp(ctx, s.deps.Writer, projection.Observed{
		ChannelID:   job.BucketChannelID,
		MsgID:       msgID,
		MsgDate:     sent.Date,
		ActorUserID: s.deps.ActorUserID,
		RawText:     sent.Text,
	})
	if err != nil {
		return fmt.Errorf("remove: project tombstone: %w", err)
	}
	switch outcome.Kind {
	case projection.OutcomeApplied, projection.OutcomeAlreadySeen, projection.OutcomeSuperseded:
		return s.commit(ctx, job, msgID)
	}
	return fmt.Errorf("remove: tombstone not projected: %s %s", outcome.Kind, outcome.RejectCode)
}

func (s *Deleter) readBack(ctx context.Context, job *jobrepo.DeleteJob, msgID int64) (tgclient.HistoryMessage, error) {
	peer, err := s.deps.Peers.PeerFor(ctx, job.BucketChannelID)
	if err != nil {
		return tgclient.HistoryMessage{}, fmt.Errorf("remove: resolve channel %d: %w: %w", job.BucketChannelID, errs.ErrSlowDown, err)
	}
	found, err := s.deps.Messages.GetMessages(ctx, peer, []int64{msgID})
	if err != nil {
		return tgclient.HistoryMessage{}, fmt.Errorf("remove: read back tombstone %d in channel %d: %w: %w", msgID, job.BucketChannelID, errs.ErrSlowDown, err)
	}
	for _, msg := range found {
		if msg.MsgID == msgID && msg.Text != "" && msg.Date > 0 {
			return msg, nil
		}
	}
	return tgclient.HistoryMessage{}, fmt.Errorf("remove: read back tombstone %d in channel %d: %w: message not returned", msgID, job.BucketChannelID, errs.ErrSlowDown)
}

func (s *Deleter) commit(ctx context.Context, job *jobrepo.DeleteJob, msgID int64) error {
	err := s.advance(ctx, job, jobrepo.DeleteCommitted, jobrepo.DeletePatch{DelMsgID: &msgID})
	if errors.Is(err, jobrepo.ErrStaleTransition) {
		current, loadErr := s.deps.Jobs.DeleteJob(ctx, job.JobID)
		if loadErr == nil && current.State == jobrepo.DeleteCommitted {
			return nil
		}
	}
	return err
}

func (s *Deleter) abort(ctx context.Context, job *jobrepo.DeleteJob) error {
	code := "version_guard"
	if job.State == jobrepo.DeleteDelAttempted {
		if err := s.advance(ctx, job, jobrepo.DeleteUncertainDel, jobrepo.DeletePatch{ErrorCode: &code}); err != nil {
			return err
		}
	}
	return s.advance(ctx, job, jobrepo.DeleteAborted, jobrepo.DeletePatch{ErrorCode: &code})
}

func (s *Deleter) advance(ctx context.Context, job *jobrepo.DeleteJob, to jobrepo.DeleteState, patch jobrepo.DeletePatch) error {
	now := s.deps.Clock.Now().UnixMilli()
	if err := s.deps.Jobs.TransitionDeleteJob(ctx, job.JobID, job.State, to, patch, now); err != nil {
		return fmt.Errorf("remove: move delete job to %s: %w", to, err)
	}
	job.State = to
	job.UpdatedAt = now
	if patch.DelHeader != nil {
		job.DelHeader = *patch.DelHeader
	}
	if patch.DelMsgID != nil {
		job.DelMsgID = *patch.DelMsgID
	}
	return nil
}

func (s *Deleter) drain(ctx context.Context, channelID int64) {
	if err := s.deps.Drainer.DrainChannel(ctx, channelID); err != nil {
		s.deps.Logger.Warn("remove: cleanup left queued for the sweeper", "channel_id", channelID)
	}
}

func formatDel(jobID, key string, target int64, writer string, now int64) (string, error) {
	header, err := wire.Format(wire.Op{Type: wire.OpDel, JobID: jobID, Key: key, PutVersion: target, Writer: writer, Timestamp: now})
	if err != nil {
		return "", fmt.Errorf("remove: format tombstone: %w", err)
	}
	return header, nil
}

func newJobID(random io.Reader) (string, error) {
	var raw [16]byte
	if _, err := io.ReadFull(random, raw[:]); err != nil {
		return "", fmt.Errorf("remove: job id: %w", err)
	}
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	text := hex.EncodeToString(raw[:])
	return text[0:8] + "-" + text[8:12] + "-" + text[12:16] + "-" + text[16:20] + "-" + text[20:32], nil
}
