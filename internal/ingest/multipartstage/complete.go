package multipartstage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/limits"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/storage/jobrepo"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

type completeFlight struct {
	listHash string
	done     chan struct{}
	meta     object.ObjectMeta
	err      error
}

type completePlan struct {
	parts     []jobrepo.MultipartPart
	plaintext int64
	md5s      [][16]byte
}

func (s *Stage) Complete(ctx context.Context, uploadID string, parts []CompletedPart, cond Conditions) (object.ObjectMeta, error) {
	if err := validateUploadID(uploadID); err != nil {
		return object.ObjectMeta{}, err
	}
	listHash := partListSHA256(parts)
	flight, leader := s.joinFlight(uploadID, listHash)
	if !leader {
		return awaitFlight(ctx, flight, listHash)
	}
	meta, err := s.runComplete(ctx, uploadID, parts, listHash, cond)
	s.finishFlight(uploadID, flight, meta, err)
	return meta, err
}

func (s *Stage) joinFlight(uploadID, listHash string) (*completeFlight, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, running := s.flights[uploadID]; running {
		return existing, false
	}
	flight := &completeFlight{listHash: listHash, done: make(chan struct{})}
	s.flights[uploadID] = flight
	return flight, true
}

func (s *Stage) finishFlight(uploadID string, flight *completeFlight, meta object.ObjectMeta, err error) {
	flight.meta, flight.err = meta, err
	s.mu.Lock()
	delete(s.flights, uploadID)
	s.mu.Unlock()
	close(flight.done)
}

func awaitFlight(ctx context.Context, flight *completeFlight, listHash string) (object.ObjectMeta, error) {
	if flight.listHash != listHash {
		return object.ObjectMeta{}, fmt.Errorf("multipartstage: complete: a different part list is committing: %w", errs.ErrUploadBusy)
	}
	select {
	case <-flight.done:
		return flight.meta, flight.err
	case <-ctx.Done():
		return object.ObjectMeta{}, fmt.Errorf("multipartstage: complete: waiting for the running job: %w", ctx.Err())
	}
}

func partListSHA256(parts []CompletedPart) string {
	digest := sha256.New()
	for _, part := range parts {
		fmt.Fprintf(digest, "%d:%s\n", part.Number, normalizeETag(part.ETag))
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func normalizeETag(etag string) string {
	return strings.ToLower(strings.Trim(etag, `"`))
}

func (s *Stage) runComplete(ctx context.Context, uploadID string, parts []CompletedPart, listHash string, cond Conditions) (object.ObjectMeta, error) {
	upload, err := s.deps.Index.GetUpload(ctx, uploadID)
	if err != nil {
		return object.ObjectMeta{}, fmt.Errorf("multipartstage: complete: %w", err)
	}
	switch upload.State {
	case jobrepo.MultipartCompleted:
		return storedResult(upload, listHash)
	case jobrepo.MultipartAborted:
		return object.ObjectMeta{}, fmt.Errorf("multipartstage: complete: %w", errs.ErrNoSuchUpload)
	}
	plan, err := s.validateListed(ctx, upload, parts)
	if err != nil {
		return object.ObjectMeta{}, err
	}
	jobID, err := s.beginCommit(ctx, upload)
	if err != nil {
		return object.ObjectMeta{}, err
	}
	return s.assemble(ctx, upload, plan, jobID, listHash, cond)
}

func storedResult(upload jobrepo.MultipartUpload, listHash string) (object.ObjectMeta, error) {
	if upload.PartListSHA256 != listHash {
		return object.ObjectMeta{}, fmt.Errorf("multipartstage: complete: %w", errs.ErrNoSuchUpload)
	}
	return object.ObjectMeta{
		ChannelID:   upload.BucketChannelID,
		Key:         upload.ObjectKey,
		PutMsgID:    upload.PutMsgID,
		JobID:       upload.JobID,
		ETag:        upload.ETag,
		ContentType: upload.ContentType,
		Encrypted:   upload.Encrypted,
	}, nil
}

func (s *Stage) validateListed(ctx context.Context, upload jobrepo.MultipartUpload, listed []CompletedPart) (completePlan, error) {
	if err := validateListedOrder(listed); err != nil {
		return completePlan{}, err
	}
	staged, err := s.listAllParts(ctx, upload.UploadID)
	if err != nil {
		return completePlan{}, err
	}
	byNumber := make(map[int]jobrepo.MultipartPart, len(staged))
	for _, row := range staged {
		byNumber[row.PartNumber] = row
	}
	var plan completePlan
	for i, want := range listed {
		row, ok := byNumber[want.Number]
		if !ok || normalizeETag(want.ETag) != row.MD5 || (want.Checksum != "" && want.Checksum != row.ChecksumValue) {
			return completePlan{}, fmt.Errorf("multipartstage: complete: part %d: %w", want.Number, errs.ErrInvalidPart)
		}
		if i < len(listed)-1 && row.Size < limits.MinMultipartPartBytes {
			return completePlan{}, fmt.Errorf("multipartstage: complete: part %d of %d bytes: %w", want.Number, row.Size, errs.ErrEntityTooSmall)
		}
		sum, err := decodeMD5(row.MD5)
		if err != nil {
			return completePlan{}, err
		}
		plan.parts = append(plan.parts, row)
		plan.md5s = append(plan.md5s, sum)
		plan.plaintext += row.Size
	}
	if exceedsStoredCeiling(upload.Encrypted, plan.plaintext) {
		return completePlan{}, fmt.Errorf("multipartstage: complete: listed parts exceed the stored size ceiling: %w", errs.ErrObjectTooLarge)
	}
	return plan, s.verifyStagedFiles(upload.UploadID, plan.parts)
}

func validateListedOrder(listed []CompletedPart) error {
	if len(listed) == 0 || len(listed) > limits.MaxMultipartParts {
		return fmt.Errorf("multipartstage: complete: %d listed parts: %w", len(listed), errs.ErrInvalidPart)
	}
	for i := 1; i < len(listed); i++ {
		if listed[i].Number <= listed[i-1].Number {
			return fmt.Errorf("multipartstage: complete: part %d after %d: %w", listed[i].Number, listed[i-1].Number, errs.ErrInvalidPartOrder)
		}
	}
	return nil
}

func decodeMD5(digest string) ([16]byte, error) {
	var sum [16]byte
	raw, err := hex.DecodeString(digest)
	if err != nil || len(raw) != len(sum) {
		return sum, fmt.Errorf("multipartstage: staged part digest: %w", jobrepo.ErrCorruptRow)
	}
	copy(sum[:], raw)
	return sum, nil
}

func (s *Stage) verifyStagedFiles(uploadID string, parts []jobrepo.MultipartPart) error {
	for _, row := range parts {
		info, err := s.deps.FS.Stat(s.partPath(uploadID, row.PartNumber))
		if err != nil {
			return fmt.Errorf("multipartstage: complete: stat part %d: %w: %w", row.PartNumber, errs.ErrInvalidPart, err)
		}
		if info.Size() != row.Size {
			return fmt.Errorf("multipartstage: complete: part %d is %d bytes on disk, %d recorded: %w", row.PartNumber, info.Size(), row.Size, errs.ErrInvalidPart)
		}
	}
	return nil
}

func (s *Stage) beginCommit(ctx context.Context, upload jobrepo.MultipartUpload) (string, error) {
	if upload.State == jobrepo.MultipartCommitting {
		return upload.JobID, nil
	}
	jobID, err := s.newJobID()
	if err != nil {
		return "", err
	}
	activity := s.deps.Clock.Now().UnixMilli()
	patch := jobrepo.MultipartPatch{JobID: &jobID, LastActivityAt: &activity}
	if err := s.deps.Index.Transition(ctx, upload.UploadID, string(jobrepo.MultipartOpen), string(jobrepo.MultipartCommitting), patch); err != nil {
		if errors.Is(err, jobrepo.ErrStaleTransition) {
			return "", fmt.Errorf("multipartstage: complete: %w: %w", errs.ErrUploadBusy, err)
		}
		return "", fmt.Errorf("multipartstage: complete: begin commit: %w", err)
	}
	return jobID, nil
}

func (s *Stage) assemble(ctx context.Context, upload jobrepo.MultipartUpload, plan completePlan, jobID, listHash string, cond Conditions) (object.ObjectMeta, error) {
	req, err := putRequestFor(upload, plan, cond)
	if err != nil {
		return object.ObjectMeta{}, s.afterPutFailure(ctx, upload.UploadID, err)
	}
	etag, err := object.MultipartETag(plan.md5s)
	if err != nil {
		return object.ObjectMeta{}, s.afterPutFailure(ctx, upload.UploadID, err)
	}
	body := newConcatReader(ctx, s, upload.UploadID, plan.parts)
	defer body.Close()
	meta, err := s.deps.Putter.PutMultipart(ctx, req, body, upload.UploadID, jobID, etag)
	if err != nil {
		return object.ObjectMeta{}, s.afterPutFailure(ctx, upload.UploadID, err)
	}
	return s.finishCompleted(ctx, upload.UploadID, meta, jobID, etag, listHash)
}

func putRequestFor(upload jobrepo.MultipartUpload, plan completePlan, cond Conditions) (object.PutRequest, error) {
	userMeta, err := unmarshalMetadata(upload.UserMetaJSON)
	if err != nil {
		return object.PutRequest{}, err
	}
	systemMeta, err := unmarshalMetadata(upload.SystemMetaJSON)
	if err != nil {
		return object.PutRequest{}, err
	}
	encryption := bucket.EncryptionNone
	if upload.Encrypted {
		encryption = bucket.EncryptionTDE1
	}
	return object.PutRequest{
		Bucket:         upload.BucketName,
		Key:            object.Key(upload.ObjectKey),
		ContentLength:  plan.plaintext,
		ContentType:    upload.ContentType,
		UserMeta:       userMeta,
		SystemMeta:     systemMeta,
		Encryption:     encryption,
		AccessKeyID:    upload.InitiatorAccessKeyID,
		IfMatch:        cond.IfMatch,
		IfNoneMatchAny: cond.IfNoneMatchAny,
	}, nil
}

func (s *Stage) finishCompleted(ctx context.Context, uploadID string, meta object.ObjectMeta, jobID, etag, listHash string) (object.ObjectMeta, error) {
	completedAt := s.deps.Clock.Now().UnixMilli()
	patch := jobrepo.MultipartPatch{ETag: &etag, PutMsgID: &meta.PutMsgID, PartListSHA256: &listHash, CompletedAt: &completedAt}
	settle := context.WithoutCancel(ctx)
	if err := s.deps.Index.Transition(settle, uploadID, string(jobrepo.MultipartCommitting), string(jobrepo.MultipartCompleted), patch); err != nil {
		return object.ObjectMeta{}, fmt.Errorf("multipartstage: complete: record completion: %w", err)
	}
	_ = s.removeUploadFiles(uploadID)
	meta.ETag = etag
	if meta.JobID == "" {
		meta.JobID = jobID
	}
	return meta, nil
}

func (s *Stage) afterPutFailure(ctx context.Context, uploadID string, putErr error) error {
	if errors.Is(putErr, tgclient.ErrSendOutcomeUnknown) {
		return fmt.Errorf("multipartstage: complete: %w: %w", errs.ErrSlowDown, putErr)
	}
	if ctx.Err() != nil || errors.Is(putErr, context.Canceled) || errors.Is(putErr, context.DeadlineExceeded) {
		return fmt.Errorf("multipartstage: complete: %w", putErr)
	}
	reopened := ""
	patch := jobrepo.MultipartPatch{JobID: &reopened}
	revertErr := s.deps.Index.Transition(context.WithoutCancel(ctx), uploadID, string(jobrepo.MultipartCommitting), string(jobrepo.MultipartOpen), patch)
	failure := fmt.Errorf("multipartstage: complete: %w", putErr)
	if errors.Is(putErr, tgclient.ErrFloodWait) && !errors.Is(putErr, errs.ErrSlowDown) {
		failure = fmt.Errorf("multipartstage: complete: %w: %w", errs.ErrSlowDown, putErr)
	}
	if revertErr != nil {
		return errors.Join(failure, fmt.Errorf("multipartstage: complete: reopen upload: %w", revertErr))
	}
	return failure
}

type concatReader struct {
	ctx      context.Context
	stage    *Stage
	uploadID string
	pending  []jobrepo.MultipartPart
	current  io.ReadCloser
}

func newConcatReader(ctx context.Context, stage *Stage, uploadID string, parts []jobrepo.MultipartPart) *concatReader {
	return &concatReader{ctx: ctx, stage: stage, uploadID: uploadID, pending: parts}
}

func (r *concatReader) Read(p []byte) (int, error) {
	for {
		if err := r.ctx.Err(); err != nil {
			return 0, err
		}
		if r.current == nil {
			if len(r.pending) == 0 {
				return 0, io.EOF
			}
			file, err := r.stage.reader.Open(r.stage.partPath(r.uploadID, r.pending[0].PartNumber))
			if err != nil {
				return 0, fmt.Errorf("multipartstage: open part %d: %w", r.pending[0].PartNumber, err)
			}
			r.current, r.pending = file, r.pending[1:]
		}
		n, err := r.current.Read(p)
		if errors.Is(err, io.EOF) {
			closeErr := r.current.Close()
			r.current = nil
			if closeErr != nil {
				return n, fmt.Errorf("multipartstage: close part: %w", closeErr)
			}
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (r *concatReader) Close() error {
	if r.current == nil {
		return nil
	}
	err := r.current.Close()
	r.current = nil
	return err
}
