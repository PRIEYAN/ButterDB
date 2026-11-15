package put

import (
	"context"
	"fmt"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/ingest/admission"
	"github.com/PRIEYAN/ButterDB/internal/projection"
	"github.com/PRIEYAN/ButterDB/internal/storage/jobrepo"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/flow"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

type commitMode int

const (
	commitFresh commitMode = iota
	commitResumed
)

type committedPut struct {
	msgID    int64
	msgDate  int64
	outcome  projection.OutcomeKind
	previous bool
}

func checkConditions(job jobrepo.UploadJob, current object.ObjectMeta, found bool) error {
	if job.IfNoneMatch && found {
		return fmt.Errorf("put: if-none-match on an existing key: %w", errs.ErrPreconditionFailed)
	}
	if job.IfMatch == "" {
		return nil
	}
	if !found || (job.IfMatch != "*" && normalizeETag(current.ETag) != job.IfMatch) {
		return fmt.Errorf("put: if-match does not match the current version: %w", errs.ErrPreconditionFailed)
	}
	return nil
}

func attemptedState(state jobrepo.UploadState) bool {
	return state == jobrepo.UploadPutAttempted || state == jobrepo.UploadUncertainPut
}

func (s *Putter) commitJob(ctx context.Context, job *jobrepo.UploadJob, budget admission.Budget, mode commitMode) (object.ObjectMeta, error) {
	done, err := s.commitLocked(ctx, job, budget, mode)
	if err != nil {
		return object.ObjectMeta{}, err
	}
	return s.afterCommit(ctx, *job, done), nil
}

func (s *Putter) commitLocked(ctx context.Context, job *jobrepo.UploadJob, budget admission.Budget, mode commitMode) (committedPut, error) {
	release, err := s.locker.Lock(ctx, objectLockKey(job.BucketChannelID, job.ObjectKey))
	if err != nil {
		return committedPut{}, err
	}
	defer release()
	current, found, err := s.currentObject(ctx, job.BucketChannelID, job.ObjectKey)
	if err != nil {
		return committedPut{}, err
	}
	var done committedPut
	if attemptedState(job.State) {
		done, err = s.finishAttempted(ctx, job, budget, mode, current, found)
	} else {
		done, err = s.attemptPut(ctx, job, budget, mode, current, found)
	}
	if err != nil {
		return committedPut{}, err
	}
	done.previous = done.previous || found
	return done, nil
}

func (s *Putter) attemptPut(ctx context.Context, job *jobrepo.UploadJob, budget admission.Budget, mode commitMode, current object.ObjectMeta, found bool) (committedPut, error) {
	if err := checkConditions(*job, current, found); err != nil {
		return committedPut{}, s.failBeforeAttempt(ctx, job, mode, err)
	}
	if err := s.versionGuard(*job, mode, current, found); err != nil {
		return committedPut{}, s.failBeforeAttempt(ctx, job, mode, err)
	}
	if err := s.deps.Admission.WaitSend(ctx, budget, flow.SendControl); err != nil {
		return committedPut{}, s.failBeforeAttempt(ctx, job, mode, err)
	}
	header, err := s.putHeader(*job)
	if err != nil {
		return committedPut{}, s.failBeforeAttempt(ctx, job, mode, err)
	}
	if err := s.transition(ctx, job, jobrepo.UploadPutAttempted, jobrepo.UploadPatch{PutHeader: &header, IncrementAttempts: true}); err != nil {
		return committedPut{}, err
	}
	return s.sendPut(ctx, job, budget)
}

func (s *Putter) finishAttempted(ctx context.Context, job *jobrepo.UploadJob, budget admission.Budget, mode commitMode, current object.ObjectMeta, found bool) (committedPut, error) {
	if found && current.JobID == job.JobID {
		return s.recordProjected(ctx, job, current.PutMsgID, current.MsgDate, projection.OutcomeAlreadySeen)
	}
	if job.PutMsgID > 0 {
		return s.projectPut(ctx, job, job.PutMsgID)
	}
	if err := s.versionGuard(*job, mode, current, found); err != nil {
		s.abort(ctx, job, err)
		return committedPut{}, err
	}
	if job.State == jobrepo.UploadUncertainPut {
		if err := s.transition(ctx, job, jobrepo.UploadPutAttempted, jobrepo.UploadPatch{IncrementAttempts: true}); err != nil {
			return committedPut{}, err
		}
	}
	return s.sendPut(ctx, job, budget)
}

func (s *Putter) versionGuard(job jobrepo.UploadJob, mode commitMode, current object.ObjectMeta, found bool) error {
	if mode != commitResumed {
		return nil
	}
	var version int64
	if found {
		version = current.PutMsgID
	}
	if version != job.BasePutMsgID {
		return fmt.Errorf("put: job %s: key version moved from %d to %d: %w", job.JobID, job.BasePutMsgID, version, errs.ErrPreconditionFailed)
	}
	return nil
}

func (s *Putter) failBeforeAttempt(ctx context.Context, job *jobrepo.UploadJob, mode commitMode, cause error) error {
	if mode == commitFresh {
		s.discardBody(ctx, *job)
	}
	s.abort(ctx, job, cause)
	return cause
}

func (s *Putter) putHeader(job jobrepo.UploadJob) (string, error) {
	userMeta, err := decodeMeta(job.UserMetaJSON)
	if err != nil {
		return "", err
	}
	systemMeta, err := decodeMeta(job.SystemMetaJSON)
	if err != nil {
		return "", err
	}
	op := wire.Op{
		Type:              wire.OpPut,
		JobID:             job.JobID,
		Key:               job.ObjectKey,
		BodyMsgID:         job.BodyMsgID,
		StoredSize:        job.StoredSize,
		Encrypted:         job.Encrypted,
		EncryptionVersion: job.EncryptionVersion,
		KeyID:             job.KeyID,
		ETag:              job.ETag,
		SHA256:            job.SHA256,
		ContentType:       job.ContentType,
		UserMeta:          userMeta,
		SystemMeta:        systemMeta,
		Writer:            job.Writer,
		Timestamp:         s.deps.Clock.Now().Unix(),
	}
	if job.Encrypted {
		op.PlaintextSize = job.PlaintextSize
	}
	header, err := wire.Format(op)
	if err != nil {
		return "", fmt.Errorf("put: job %s: format put op: %w", job.JobID, err)
	}
	return header, nil
}

func (s *Putter) sendPut(ctx context.Context, job *jobrepo.UploadJob, budget admission.Budget) (committedPut, error) {
	randomID, err := s.randomID(job.JobID, putPurpose)
	if err != nil {
		return committedPut{}, s.settlePutFailure(ctx, job, err)
	}
	peer, err := s.deps.Peers.ResolveChannel(ctx, job.BucketChannelID)
	if err != nil {
		return committedPut{}, s.settlePutFailure(ctx, job, fmt.Errorf("put: resolve channel %d: %w", job.BucketChannelID, err))
	}
	msgID, err := s.retrySend(ctx, budget, func() (int64, error) {
		return s.deps.Controls.SendControlIdempotent(ctx, tgclient.SendControlRequest{Peer: peer, Text: job.PutHeader, RandomID: randomID})
	})
	if err != nil {
		return committedPut{}, s.settlePutFailure(ctx, job, err)
	}
	return s.projectPut(ctx, job, msgID)
}

func (s *Putter) settlePutFailure(ctx context.Context, job *jobrepo.UploadJob, cause error) error {
	if err := s.markUncertain(ctx, job, jobrepo.UploadUncertainPut, cause, jobrepo.UploadPatch{}); err != nil {
		return err
	}
	return &OutcomeUnknownError{JobID: job.JobID, Err: cause}
}

func (s *Putter) projectPut(ctx context.Context, job *jobrepo.UploadJob, msgID int64) (committedPut, error) {
	sent, err := s.readBack(ctx, job.BucketChannelID, msgID)
	if err != nil {
		return committedPut{}, s.settleProjectionFailure(ctx, job, msgID, err)
	}
	text := job.PutHeader
	if sent.Text != "" {
		text = sent.Text
	}
	outcome, err := s.project(ctx, projection.Observed{
		ChannelID:   job.BucketChannelID,
		MsgID:       msgID,
		MsgDate:     sent.Date,
		ActorUserID: s.deps.Config.ActorUserID,
		RawText:     text,
	})
	if err != nil {
		return committedPut{}, s.settleProjectionFailure(ctx, job, msgID, err)
	}
	return s.recordProjected(ctx, job, msgID, sent.Date, outcome.Kind)
}

func (s *Putter) recordProjected(ctx context.Context, job *jobrepo.UploadJob, msgID, msgDate int64, kind projection.OutcomeKind) (committedPut, error) {
	if err := s.transition(ctx, job, jobrepo.UploadCommitted, jobrepo.UploadPatch{PutMsgID: &msgID}); err != nil {
		return committedPut{}, &ControlProjectionError{MsgID: msgID, Err: err}
	}
	return committedPut{msgID: msgID, msgDate: msgDate, outcome: kind}, nil
}

func (s *Putter) settleProjectionFailure(ctx context.Context, job *jobrepo.UploadJob, msgID int64, cause error) error {
	if job.State == jobrepo.UploadPutAttempted {
		code := errs.Code(cause)
		patch := jobrepo.UploadPatch{PutMsgID: &msgID, ErrorCode: &code}
		if err := s.transition(ctx, job, jobrepo.UploadUncertainPut, patch); err != nil {
			s.deps.Logger.Error("put: record accepted put message failed", "job_id", job.JobID, "msg_id", msgID, "error", err)
		}
	}
	return &ControlProjectionError{MsgID: msgID, Err: cause}
}

func (s *Putter) afterCommit(ctx context.Context, job jobrepo.UploadJob, done committedPut) object.ObjectMeta {
	meta := s.resultMeta(ctx, job, done)
	s.removeSpool(job.JobID)
	if done.previous || done.outcome == projection.OutcomeSuperseded {
		if err := s.deps.Cleanup.DrainChannel(context.WithoutCancel(ctx), job.BucketChannelID); err != nil {
			s.deps.Logger.Warn("put: superseded message cleanup failed, the sweeper retries", "job_id", job.JobID, "error", err)
		}
	}
	return meta
}

func (s *Putter) resultMeta(ctx context.Context, job jobrepo.UploadJob, done committedPut) object.ObjectMeta {
	current, found, err := s.currentObject(context.WithoutCancel(ctx), job.BucketChannelID, job.ObjectKey)
	if err == nil && found && current.PutMsgID == done.msgID {
		return current
	}
	meta, _ := objectMetaFromJob(job)
	meta.PutMsgID = done.msgID
	meta.MsgDate = done.msgDate
	return meta
}

func objectMetaFromJob(job jobrepo.UploadJob) (object.ObjectMeta, error) {
	userMeta, err := decodeMeta(job.UserMetaJSON)
	if err != nil {
		return object.ObjectMeta{}, err
	}
	systemMeta, err := decodeMeta(job.SystemMetaJSON)
	if err != nil {
		return object.ObjectMeta{}, err
	}
	return object.ObjectMeta{
		ChannelID:         job.BucketChannelID,
		Key:               job.ObjectKey,
		PutMsgID:          job.PutMsgID,
		BodyMsgID:         job.BodyMsgID,
		JobID:             job.JobID,
		StoredSize:        job.StoredSize,
		PlaintextSize:     job.PlaintextSize,
		ETag:              job.ETag,
		SHA256:            job.SHA256,
		ContentType:       job.ContentType,
		UserMeta:          userMeta,
		SystemMeta:        systemMeta,
		Encrypted:         job.Encrypted,
		EncryptionVersion: job.EncryptionVersion,
		KeyID:             job.KeyID,
		Writer:            job.Writer,
	}, nil
}
