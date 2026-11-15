package put

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/ingest/admission"
	"github.com/PRIEYAN/ButterDB/internal/ingest/spool"
	"github.com/PRIEYAN/ButterDB/internal/storage/jobrepo"
)

type putOptions struct {
	multipartUploadID string
	jobID             string
	etag              string
}

func (s *Putter) Put(ctx context.Context, req object.PutRequest, body io.Reader) (object.ObjectMeta, error) {
	return s.putObject(ctx, req, body, putOptions{})
}

func (s *Putter) PutMultipart(ctx context.Context, req object.PutRequest, body io.Reader, uploadID, jobID, etag string) (object.ObjectMeta, error) {
	if uploadID == "" || len(jobID) != 36 {
		return object.ObjectMeta{}, fmt.Errorf("put: %w: multipart upload and job ids are required", ErrInvalidRequest)
	}
	if err := object.ValidateETag(etag); err != nil || !object.IsMultipartETag(etag) {
		return object.ObjectMeta{}, fmt.Errorf("put: %w: multipart etag", ErrInvalidRequest)
	}
	return s.putObject(ctx, req, body, putOptions{multipartUploadID: uploadID, jobID: jobID, etag: etag})
}

func (s *Putter) putObject(ctx context.Context, req object.PutRequest, body io.Reader, opts putOptions) (object.ObjectMeta, error) {
	if body == nil {
		return object.ObjectMeta{}, fmt.Errorf("put: %w: body", errs.ErrMissingDependency)
	}
	p, err := planRequest(req)
	if err != nil {
		return object.ObjectMeta{}, err
	}
	if opts.multipartUploadID != "" {
		p.kind = jobrepo.KindMultipart
		p.multipartUploadID = opts.multipartUploadID
		p.etag = opts.etag
	}
	info, found, err := s.deps.Buckets.Lookup(ctx, bucket.BucketName(req.Bucket))
	if err != nil {
		return object.ObjectMeta{}, fmt.Errorf("put: look up bucket: %w", err)
	}
	if !found {
		return object.ObjectMeta{}, fmt.Errorf("put: bucket %q: %w", req.Bucket, errs.ErrNoSuchBucket)
	}
	res, err := p.resolve(info)
	if err != nil {
		return object.ObjectMeta{}, err
	}
	budget := admission.Budget{Deadline: s.deps.Clock.Now().Add(s.deps.Config.RequestBudget)}
	releaseSlot, err := s.deps.Admission.AcquirePut(ctx, budget, res.scope)
	if err != nil {
		return object.ObjectMeta{}, err
	}
	defer releaseSlot()
	releaseGate, err := s.deps.Gates.AcquireShared(ctx, info.ChannelID)
	if err != nil {
		return object.ObjectMeta{}, err
	}
	defer releaseGate()
	if err := s.deps.Authority.RequireWriteAuthority(ctx, info.ChannelID); err != nil {
		return object.ObjectMeta{}, err
	}
	if existing, ok, err := s.existingJob(ctx, opts.jobID); err != nil || ok {
		if err != nil {
			return object.ObjectMeta{}, err
		}
		return s.resumeJob(ctx, &existing, budget, commitResumed)
	}
	return s.putNew(ctx, res, body, opts, budget)
}

func (s *Putter) existingJob(ctx context.Context, jobID string) (jobrepo.UploadJob, bool, error) {
	if jobID == "" {
		return jobrepo.UploadJob{}, false, nil
	}
	job, err := s.deps.Jobs.GetJob(ctx, jobID)
	if errors.Is(err, jobrepo.ErrJobNotFound) {
		return jobrepo.UploadJob{}, false, nil
	}
	if err != nil {
		return jobrepo.UploadJob{}, false, fmt.Errorf("put: load job %s: %w", jobID, err)
	}
	return job, true, nil
}

func (s *Putter) putNew(ctx context.Context, res resolved, body io.Reader, opts putOptions, budget admission.Budget) (object.ObjectMeta, error) {
	probe := jobrepo.UploadJob{IfMatch: res.ifMatch, IfNoneMatch: res.ifNoneMatch}
	current, found, err := s.currentObject(ctx, res.info.ChannelID, string(res.key))
	if err != nil {
		return object.ObjectMeta{}, err
	}
	if err := checkConditions(probe, current, found); err != nil {
		return object.ObjectMeta{}, err
	}
	params, err := s.encryptionParamsFor(ctx, res)
	if err != nil {
		return object.ObjectMeta{}, err
	}
	jobID := opts.jobID
	if jobID == "" {
		if jobID, err = newJobID(s.deps.Random); err != nil {
			return object.ObjectMeta{}, err
		}
	}
	file, err := s.deps.Spool.Write(ctx, jobID, body, res.plaintextSize, maxPlaintextFor(res.encrypted))
	if err != nil {
		return object.ObjectMeta{}, fmt.Errorf("put: spool body: %w", err)
	}
	if meta, replayed, err := s.replayIdempotent(ctx, res, file); err != nil || replayed {
		s.removeSpool(jobID)
		return meta, err
	}
	job, err := s.newJob(res, jobID, file, params)
	if err != nil {
		s.removeSpool(jobID)
		return object.ObjectMeta{}, err
	}
	job.BasePutMsgID = current.PutMsgID
	if err := s.deps.Jobs.CreateJob(ctx, job, s.idempotencyRecord(res, job, file)); err != nil {
		s.removeSpool(jobID)
		return object.ObjectMeta{}, fmt.Errorf("put: create job %s: %w", jobID, err)
	}
	return s.resumeJob(ctx, &job, budget, commitFresh)
}

func (s *Putter) idempotencyRecord(res resolved, job jobrepo.UploadJob, file spool.File) *jobrepo.IdempotencyRecord {
	if res.idempotencyKey == "" {
		return nil
	}
	return &jobrepo.IdempotencyRecord{
		Scope:           res.scope,
		IdempotencyKey:  res.idempotencyKey,
		BucketChannelID: job.BucketChannelID,
		ObjectKey:       job.ObjectKey,
		BodySHA256:      hex.EncodeToString(file.SHA256[:]),
		JobID:           job.JobID,
		CreatedAt:       job.CreatedAt,
		ExpiresAt:       job.CreatedAt + int64(idempotencyTTL.Seconds()),
	}
}

func (s *Putter) replayIdempotent(ctx context.Context, res resolved, file spool.File) (object.ObjectMeta, bool, error) {
	if res.idempotencyKey == "" {
		return object.ObjectMeta{}, false, nil
	}
	record, found, err := s.deps.Jobs.LookupIdempotency(ctx, res.scope, res.idempotencyKey)
	if err != nil {
		return object.ObjectMeta{}, false, fmt.Errorf("put: look up idempotency record: %w", err)
	}
	if !found || record.ExpiresAt <= s.deps.Clock.Now().Unix() {
		return object.ObjectMeta{}, false, nil
	}
	if record.BucketChannelID != res.info.ChannelID || record.ObjectKey != string(res.key) || record.BodySHA256 != hex.EncodeToString(file.SHA256[:]) {
		return object.ObjectMeta{}, false, fmt.Errorf("put: %w", ErrIdempotencyConflict)
	}
	prior, err := s.deps.Jobs.GetJob(ctx, record.JobID)
	if err != nil {
		return object.ObjectMeta{}, false, fmt.Errorf("put: load idempotent job %s: %w", record.JobID, err)
	}
	switch prior.State {
	case jobrepo.UploadCommitted:
		return s.resultMeta(ctx, prior, committedPut{msgID: prior.PutMsgID}), true, nil
	case jobrepo.UploadAborted:
		return object.ObjectMeta{}, false, nil
	}
	return object.ObjectMeta{}, false, &OutcomeUnknownError{JobID: prior.JobID, Err: ErrCommitOutcomeUnknown}
}
