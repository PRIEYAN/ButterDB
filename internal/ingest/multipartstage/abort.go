package multipartstage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/storage/jobrepo"
)

func (s *Stage) Abort(ctx context.Context, uploadID string) error {
	if err := validateUploadID(uploadID); err != nil {
		return err
	}
	upload, err := s.deps.Index.GetUpload(ctx, uploadID)
	if err != nil {
		return fmt.Errorf("multipartstage: abort: %w", err)
	}
	if err := requireOpen(upload); err != nil {
		return err
	}
	if err := s.discardUpload(ctx, uploadID); err != nil {
		if errors.Is(err, jobrepo.ErrStaleTransition) {
			return fmt.Errorf("multipartstage: abort: %w: %w", errs.ErrUploadBusy, err)
		}
		return err
	}
	return nil
}

func (s *Stage) SweepIdle(ctx context.Context, idle time.Duration) (int, error) {
	cutoff := s.deps.Clock.Now().Add(-idle)
	aborted := 0
	for {
		ids, err := s.deps.Index.IdleBefore(ctx, cutoff, indexPageSize)
		if err != nil {
			return aborted, fmt.Errorf("multipartstage: sweep idle: %w", err)
		}
		progress, err := s.discardIdle(ctx, ids)
		aborted += progress
		if err != nil || progress == 0 {
			return aborted, err
		}
	}
}

func (s *Stage) discardIdle(ctx context.Context, ids []string) (int, error) {
	discarded := 0
	for _, id := range ids {
		if err := validateUploadID(id); err != nil {
			continue
		}
		err := s.discardUpload(ctx, id)
		if errors.Is(err, jobrepo.ErrStaleTransition) || errors.Is(err, errs.ErrNoSuchUpload) {
			continue
		}
		if err != nil {
			return discarded, err
		}
		discarded++
	}
	return discarded, nil
}

func (s *Stage) discardUpload(ctx context.Context, uploadID string) error {
	err := s.deps.Index.Transition(ctx, uploadID, string(jobrepo.MultipartOpen), string(jobrepo.MultipartAborted), jobrepo.MultipartPatch{})
	if err != nil {
		return fmt.Errorf("multipartstage: abort upload: %w", err)
	}
	if err := s.removeUploadFiles(uploadID); err != nil {
		return err
	}
	if err := s.deps.Index.Delete(ctx, uploadID); err != nil {
		return fmt.Errorf("multipartstage: delete upload rows: %w", err)
	}
	return nil
}
