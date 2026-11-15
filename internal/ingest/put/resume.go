package put

import (
	"context"
	"fmt"

	"github.com/PRIEYAN/ButterDB/internal/domain/limits"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/ingest/admission"
	"github.com/PRIEYAN/ButterDB/internal/storage/jobrepo"
)

const idempotencyTTL = limits.IdempotencyTTL

func (s *Putter) Resume(ctx context.Context, jobID string) error {
	job, err := s.deps.Jobs.GetJob(ctx, jobID)
	if err != nil {
		return fmt.Errorf("put: load job %s: %w", jobID, err)
	}
	if job.State == jobrepo.UploadCommitted || job.State == jobrepo.UploadAborted {
		return nil
	}
	if err := s.deps.Authority.RequireWriteAuthority(ctx, job.BucketChannelID); err != nil {
		return err
	}
	releaseGate, err := s.deps.Gates.AcquireShared(ctx, job.BucketChannelID)
	if err != nil {
		return err
	}
	defer releaseGate()
	budget := admission.Budget{Deadline: s.deps.Clock.Now().Add(s.deps.Config.RequestBudget)}
	_, err = s.resumeJob(ctx, &job, budget, commitResumed)
	return err
}

func (s *Putter) resumeJob(ctx context.Context, job *jobrepo.UploadJob, budget admission.Budget, mode commitMode) (object.ObjectMeta, error) {
	switch job.State {
	case jobrepo.UploadCommitted:
		return s.resultMeta(ctx, *job, committedPut{msgID: job.PutMsgID}), nil
	case jobrepo.UploadAborted:
		return object.ObjectMeta{}, fmt.Errorf("put: job %s: %w", job.JobID, ErrJobAborted)
	case jobrepo.UploadPrepared, jobrepo.UploadBodySending, jobrepo.UploadUncertainBody:
		if err := s.sendBodyIfAny(ctx, job, budget); err != nil {
			return object.ObjectMeta{}, err
		}
	case jobrepo.UploadBodySent:
		if err := s.projectBody(ctx, *job); err != nil {
			return object.ObjectMeta{}, err
		}
	}
	return s.commitJob(ctx, job, budget, mode)
}

func (s *Putter) sendBodyIfAny(ctx context.Context, job *jobrepo.UploadJob, budget admission.Budget) error {
	if !job.Encrypted && job.StoredSize == 0 && job.State == jobrepo.UploadPrepared {
		return nil
	}
	return s.sendBody(ctx, job, budget)
}
