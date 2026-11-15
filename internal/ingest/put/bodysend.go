package put

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/ingest/admission"
	"github.com/PRIEYAN/ButterDB/internal/ingest/spool"
	"github.com/PRIEYAN/ButterDB/internal/projection"
	"github.com/PRIEYAN/ButterDB/internal/storage/jobrepo"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/flow"
)

const (
	maxUnknownOutcomeSends = 3
	maxFloodWaitRetries    = 8
)

func floodWaitOf(err error) (*tgclient.FloodWaitError, bool) {
	var flood *tgclient.FloodWaitError
	if errors.As(err, &flood) {
		return flood, true
	}
	return nil, false
}

func (s *Putter) retrySend(ctx context.Context, budget admission.Budget, send func() (int64, error)) (int64, error) {
	var lastUnknown error
	unknown, floods := 0, 0
	for {
		if err := ctx.Err(); err != nil {
			return 0, joinUnknown(err, lastUnknown)
		}
		msgID, err := send()
		switch {
		case msgID > 0:
			if err != nil {
				s.deps.Logger.Warn("put: send reported an error after the message was accepted", "msg_id", msgID)
			}
			return msgID, nil
		case err == nil:
			unknown++
			lastUnknown = fmt.Errorf("put: %w: send returned no message id", tgclient.ErrSendOutcomeUnknown)
		default:
			if flood, ok := floodWaitOf(err); ok {
				if floods >= maxFloodWaitRetries {
					return 0, joinUnknown(err, lastUnknown)
				}
				floods++
				if waitErr := s.deps.Admission.WaitFlood(ctx, budget, flood.Duration); waitErr != nil {
					return 0, joinUnknown(waitErr, lastUnknown)
				}
				continue
			}
			if isDefinitelyNotSent(err) {
				return 0, joinUnknown(err, lastUnknown)
			}
			unknown++
			lastUnknown = err
			if !isUnknownOutcome(err) {
				lastUnknown = fmt.Errorf("put: %w: %w", tgclient.ErrSendOutcomeUnknown, err)
			}
		}
		if unknown >= maxUnknownOutcomeSends {
			return 0, lastUnknown
		}
	}
}

func isDefinitelyNotSent(err error) bool {
	return errors.Is(err, tgclient.ErrChannelUnavailable) || errors.Is(err, tgclient.ErrZeroAccessHash) ||
		errors.Is(err, tgclient.ErrNotAuthorized)
}

func joinUnknown(err, lastUnknown error) error {
	if lastUnknown == nil || isUnknownOutcome(err) {
		return err
	}
	return errors.Join(err, lastUnknown)
}

func (s *Putter) spoolFile(job jobrepo.UploadJob) (spool.File, error) {
	file := spool.File{Path: job.SpoolPath, Size: job.PlaintextSize}
	digest, err := hex.DecodeString(job.SHA256)
	if err != nil || len(digest) != len(file.SHA256) {
		return spool.File{}, fmt.Errorf("put: job %s: stored sha256 is corrupt: %w", job.JobID, jobrepo.ErrCorruptRow)
	}
	copy(file.SHA256[:], digest)
	return file, nil
}

func (s *Putter) verifySpool(file spool.File, job jobrepo.UploadJob, src io.ReadSeeker) error {
	hash := sha256.New()
	copied, err := io.Copy(hash, src)
	if err != nil {
		return fmt.Errorf("put: job %s: read spool: %w", job.JobID, err)
	}
	if copied != job.PlaintextSize || hex.EncodeToString(hash.Sum(nil)) != job.SHA256 {
		return fmt.Errorf("put: job %s: spool content changed: %w", job.JobID, errs.ErrBadDigest)
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("put: job %s: rewind spool: %w", job.JobID, err)
	}
	return nil
}

func (s *Putter) openBody(ctx context.Context, job jobrepo.UploadJob) (io.ReadSeeker, func(), error) {
	file, err := s.spoolFile(job)
	if err != nil {
		return nil, nil, err
	}
	plain, err := s.deps.Spool.Open(file)
	if err != nil {
		return nil, nil, fmt.Errorf("put: job %s: open spool: %w", job.JobID, err)
	}
	if err := s.verifySpool(file, job, plain); err != nil {
		_ = plain.Close()
		return nil, nil, err
	}
	if !job.Encrypted {
		return plain, func() { _ = plain.Close() }, nil
	}
	return s.openEncrypted(ctx, job, plain)
}

func (s *Putter) sendBody(ctx context.Context, job *jobrepo.UploadJob, budget admission.Budget) error {
	if job.State != jobrepo.UploadPrepared && job.State != jobrepo.UploadBodySending && job.State != jobrepo.UploadUncertainBody {
		return fmt.Errorf("put: job %s: cannot send a body from state %s", job.JobID, job.State)
	}
	body, closeBody, err := s.openBody(ctx, *job)
	if err != nil {
		s.abortFailedBody(ctx, job, err)
		return err
	}
	defer closeBody()
	if err := s.deps.Admission.WaitSend(ctx, budget, flow.SendDocument); err != nil {
		s.abortFailedBody(ctx, job, err)
		return err
	}
	if job.State == jobrepo.UploadPrepared {
		if err := s.transition(ctx, job, jobrepo.UploadBodySending, jobrepo.UploadPatch{IncrementAttempts: true}); err != nil {
			return err
		}
	}
	msgID, err := s.deliverBody(ctx, *job, body, budget)
	if err != nil {
		return s.settleBodyFailure(ctx, job, err)
	}
	if err := s.receiptNotOwned(ctx, *job, msgID); err != nil {
		return s.settleBodyFailure(ctx, job, err)
	}
	if err := s.transition(ctx, job, jobrepo.UploadBodySent, jobrepo.UploadPatch{BodyMsgID: &msgID}); err != nil {
		return err
	}
	return s.projectBody(ctx, *job)
}

func (s *Putter) abortFailedBody(ctx context.Context, job *jobrepo.UploadJob, cause error) {
	if job.State != jobrepo.UploadPrepared && job.State != jobrepo.UploadBodySending {
		return
	}
	s.abort(ctx, job, cause)
}

func (s *Putter) settleBodyFailure(ctx context.Context, job *jobrepo.UploadJob, cause error) error {
	if isUnknownOutcome(cause) {
		if err := s.markUncertain(ctx, job, jobrepo.UploadUncertainBody, cause, jobrepo.UploadPatch{}); err != nil {
			return err
		}
		return &OutcomeUnknownError{JobID: job.JobID, Err: cause}
	}
	s.abortFailedBody(ctx, job, cause)
	return cause
}

func (s *Putter) deliverBody(ctx context.Context, job jobrepo.UploadJob, body io.ReadSeeker, budget admission.Budget) (int64, error) {
	caption, err := s.bodyCaption(job)
	if err != nil {
		return 0, err
	}
	randomID, err := s.randomID(job.JobID, bodyPurpose)
	if err != nil {
		return 0, err
	}
	peer, err := s.deps.Peers.ResolveChannel(ctx, job.BucketChannelID)
	if err != nil {
		return 0, fmt.Errorf("put: resolve channel %d: %w", job.BucketChannelID, err)
	}
	refreshed := false
	return s.retrySend(ctx, budget, func() (int64, error) {
		if _, err := body.Seek(0, io.SeekStart); err != nil {
			return 0, fmt.Errorf("put: rewind body: %w", err)
		}
		result, err := s.deps.Bodies.SendFileIdempotent(ctx, tgclient.SendFileRequest{
			Peer:     peer,
			Caption:  caption,
			FileName: job.JobID + ".bin",
			Size:     job.StoredSize,
			Body:     body,
			RandomID: randomID,
		})
		if errors.Is(err, tgclient.ErrChannelUnavailable) && !refreshed && result.MsgID == 0 {
			refreshed = true
			if peer, err = s.deps.Peers.ResolveChannel(ctx, job.BucketChannelID); err != nil {
				return 0, fmt.Errorf("put: resolve channel %d: %w", job.BucketChannelID, err)
			}
			return 0, tgclient.ErrChannelUnavailable
		}
		return result.MsgID, err
	})
}

func (s *Putter) receiptNotOwned(ctx context.Context, job jobrepo.UploadJob, msgID int64) error {
	return s.refuseOwnedMessage(context.WithoutCancel(ctx), job, msgID)
}

func (s *Putter) projectBody(ctx context.Context, job jobrepo.UploadJob) error {
	caption, err := s.bodyCaption(job)
	if err != nil {
		return err
	}
	sent, err := s.readBack(ctx, job.BucketChannelID, job.BodyMsgID)
	if err != nil {
		return fmt.Errorf("put: job %s: read back body message: %w", job.JobID, err)
	}
	if sent.Text != "" {
		caption = sent.Text
	}
	_, err = s.project(ctx, projection.Observed{
		ChannelID:   job.BucketChannelID,
		MsgID:       job.BodyMsgID,
		MsgDate:     sent.Date,
		IsDocument:  true,
		ActorUserID: s.deps.Config.ActorUserID,
		RawText:     caption,
	})
	if err != nil {
		return fmt.Errorf("put: job %s: project body observation: %w", job.JobID, err)
	}
	return nil
}

func (s *Putter) readBack(ctx context.Context, channelID, msgID int64) (tgclient.HistoryMessage, error) {
	durable := context.WithoutCancel(ctx)
	peer, err := s.deps.Peers.ResolveChannel(durable, channelID)
	if err != nil {
		return tgclient.HistoryMessage{}, fmt.Errorf("put: resolve channel %d: %w", channelID, err)
	}
	found, err := s.deps.Messages.GetMessages(durable, peer, []int64{msgID})
	if err != nil {
		return tgclient.HistoryMessage{}, fmt.Errorf("put: read message %d: %w", msgID, err)
	}
	for _, msg := range found {
		if msg.MsgID == msgID && msg.Date > 0 {
			return msg, nil
		}
	}
	return tgclient.HistoryMessage{}, fmt.Errorf("put: message %d: %w", msgID, tgclient.ErrMessageNotFound)
}

func (s *Putter) project(ctx context.Context, obs projection.Observed) (projection.Outcome, error) {
	outcome, err := s.deps.Projector.ProjectFromOp(context.WithoutCancel(ctx), s.deps.Writer, obs)
	if err != nil {
		return projection.Outcome{}, err
	}
	switch outcome.Kind {
	case projection.OutcomeApplied, projection.OutcomeAlreadySeen, projection.OutcomeSuperseded:
		return outcome, nil
	case projection.OutcomeRejected:
		if outcome.RejectCode == projection.RejectBucketNotFound {
			return outcome, fmt.Errorf("put: projection rejected the op: %w", errs.ErrNoSuchBucket)
		}
		return outcome, fmt.Errorf("%w: %s", ErrProjectionRejected, outcome.RejectCode)
	}
	return outcome, fmt.Errorf("%w: outcome %s", ErrProjectionRejected, outcome.Kind)
}
