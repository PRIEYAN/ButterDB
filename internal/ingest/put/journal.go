package put

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/ingest/spool"
	"github.com/PRIEYAN/ButterDB/internal/storage/jobrepo"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

const (
	bodyPurpose = "body"
	putPurpose  = "put"
)

type encryptionParams struct {
	keyID       string
	fileSalt    []byte
	noncePrefix []byte
}

func metaJSON(meta map[string]string) (string, error) {
	if len(meta) == 0 {
		return "{}", nil
	}
	encoded, err := json.Marshal(meta)
	if err != nil {
		return "", fmt.Errorf("put: encode metadata: %w", err)
	}
	return string(encoded), nil
}

func decodeMeta(raw string) (map[string]string, error) {
	meta := map[string]string{}
	if raw == "" || raw == "{}" {
		return meta, nil
	}
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		return nil, fmt.Errorf("put: decode metadata: %w", err)
	}
	return meta, nil
}

func (s *Putter) newJob(res resolved, jobID string, file spool.File, params *encryptionParams) (jobrepo.UploadJob, error) {
	userJSON, err := metaJSON(res.userMeta)
	if err != nil {
		return jobrepo.UploadJob{}, err
	}
	systemJSON, err := metaJSON(res.systemMeta)
	if err != nil {
		return jobrepo.UploadJob{}, err
	}
	etag := res.etag
	if etag == "" {
		etag = object.SingleETag(file.MD5)
	}
	now := s.deps.Clock.Now().UnixMilli()
	job := jobrepo.UploadJob{
		JobID:             jobID,
		Kind:              res.kind,
		Origin:            res.origin,
		Scope:             res.scope,
		Writer:            res.writer,
		BucketChannelID:   res.info.ChannelID,
		BucketName:        string(res.info.Name),
		ObjectKey:         string(res.key),
		MultipartUploadID: res.multipartUploadID,
		State:             jobrepo.UploadPrepared,
		IfMatch:           res.ifMatch,
		IfNoneMatch:       res.ifNoneMatch,
		SpoolPath:         file.Path,
		PlaintextSize:     file.Size,
		StoredSize:        res.storedSize,
		ETag:              etag,
		SHA256:            hex.EncodeToString(file.SHA256[:]),
		ContentType:       res.contentType,
		UserMetaJSON:      userJSON,
		SystemMetaJSON:    systemJSON,
		Encrypted:         res.encrypted,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if params != nil {
		job.EncryptionVersion = 1
		job.KeyID = params.keyID
		job.FileSalt = params.fileSalt
		job.NoncePrefix = params.noncePrefix
	}
	return job, nil
}

func (s *Putter) transition(ctx context.Context, job *jobrepo.UploadJob, to jobrepo.UploadState, patch jobrepo.UploadPatch) error {
	durable := context.WithoutCancel(ctx)
	if err := s.deps.Jobs.TransitionJob(durable, job.JobID, job.State, to, patch); err != nil {
		return fmt.Errorf("put: job %s %s to %s: %w", job.JobID, job.State, to, err)
	}
	job.State = to
	job.UpdatedAt = s.deps.Clock.Now().UnixMilli()
	if patch.SealedPath != nil {
		job.SealedPath = *patch.SealedPath
	}
	if patch.BodyMsgID != nil {
		job.BodyMsgID = *patch.BodyMsgID
	}
	if patch.PutHeader != nil {
		job.PutHeader = *patch.PutHeader
	}
	if patch.PutMsgID != nil {
		job.PutMsgID = *patch.PutMsgID
	}
	if patch.ErrorCode != nil {
		job.ErrorCode = *patch.ErrorCode
	}
	if patch.IncrementAttempts {
		job.Attempts++
	}
	return nil
}

func (s *Putter) abort(ctx context.Context, job *jobrepo.UploadJob, cause error) {
	code := errs.Code(cause)
	patch := jobrepo.UploadPatch{ErrorCode: &code}
	if job.State == jobrepo.UploadPutAttempted {
		if err := s.transition(ctx, job, jobrepo.UploadUncertainPut, jobrepo.UploadPatch{}); err != nil {
			s.deps.Logger.Error("put: mark job uncertain before abort failed", "job_id", job.JobID, "error", err)
			return
		}
	}
	if err := s.transition(ctx, job, jobrepo.UploadAborted, patch); err != nil {
		s.deps.Logger.Error("put: abort job failed", "job_id", job.JobID, "error", err)
		return
	}
	s.removeSpool(job.JobID)
}

func (s *Putter) removeSpool(jobID string) {
	if err := s.deps.Spool.Remove(jobID); err != nil {
		s.deps.Logger.Warn("put: remove spool file failed", "job_id", jobID, "error", err)
	}
}

func (s *Putter) markUncertain(ctx context.Context, job *jobrepo.UploadJob, to jobrepo.UploadState, cause error, patch jobrepo.UploadPatch) error {
	code := errs.Code(cause)
	patch.ErrorCode = &code
	if job.State == to {
		return nil
	}
	if err := s.transition(ctx, job, to, patch); err != nil {
		return err
	}
	return &OutcomeUnknownError{JobID: job.JobID, Err: cause}
}

func (s *Putter) discardBody(ctx context.Context, job jobrepo.UploadJob) {
	if job.BodyMsgID <= 0 {
		return
	}
	durable := context.WithoutCancel(ctx)
	if err := s.refuseOwnedMessage(durable, job, job.BodyMsgID); err != nil {
		s.deps.Logger.Error("put: refusing to delete a message owned by a live object", "job_id", job.JobID, "msg_id", job.BodyMsgID)
		return
	}
	peer, err := s.deps.Peers.ResolveChannel(durable, job.BucketChannelID)
	if err != nil {
		s.deps.Logger.Warn("put: resolve channel for body cleanup failed", "job_id", job.JobID, "error", err)
		return
	}
	if err := s.deps.Deleter.DeleteMessages(durable, peer, []int64{job.BodyMsgID}); err != nil {
		s.deps.Logger.Warn("put: discard body failed, the sweeper retries", "job_id", job.JobID, "msg_id", job.BodyMsgID, "error", err)
	}
}

func (s *Putter) refuseOwnedMessage(ctx context.Context, job jobrepo.UploadJob, msgID int64) error {
	current, found, err := s.currentObject(ctx, job.BucketChannelID, job.ObjectKey)
	if err != nil {
		return err
	}
	if found && current.JobID != job.JobID && (current.BodyMsgID == msgID || current.PutMsgID == msgID) {
		return fmt.Errorf("put: message %d is owned by a live object: %w", msgID, ErrProjectionRejected)
	}
	return nil
}

func (s *Putter) currentObject(ctx context.Context, channelID int64, key string) (object.ObjectMeta, bool, error) {
	current, err := s.deps.Objects.Get(ctx, channelID, key)
	if errors.Is(err, errs.ErrNoSuchKey) {
		return object.ObjectMeta{}, false, nil
	}
	if err != nil {
		return object.ObjectMeta{}, false, fmt.Errorf("put: load current version of %q: %w", key, err)
	}
	return current, true, nil
}

func (s *Putter) randomID(jobID, purpose string) (int64, error) {
	id, err := s.deps.StableRandomID(jobID, purpose)
	if err != nil {
		return 0, fmt.Errorf("put: random id for %s: %w", purpose, err)
	}
	return id, nil
}

func (s *Putter) bodyCaption(job jobrepo.UploadJob) (string, error) {
	caption, err := wire.Format(wire.Op{
		Type:              wire.OpBody,
		JobID:             job.JobID,
		StoredSize:        job.StoredSize,
		Encrypted:         job.Encrypted,
		EncryptionVersion: job.EncryptionVersion,
	})
	if err != nil {
		return "", fmt.Errorf("put: format body caption: %w", err)
	}
	return caption, nil
}

func isUnknownOutcome(err error) bool {
	return errors.Is(err, tgclient.ErrSendOutcomeUnknown)
}
