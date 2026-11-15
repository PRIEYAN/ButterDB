package multipartstage

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/limits"
	"github.com/PRIEYAN/ButterDB/internal/storage/jobrepo"
)

type stagedFile struct {
	tempPath string
	md5Hex   string
}

type contextReader struct {
	ctx context.Context
	src io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.src.Read(p)
}

func (s *Stage) PutPart(ctx context.Context, uploadID string, partNumber int, size int64, body io.Reader) (Part, error) {
	if err := validateUploadID(uploadID); err != nil {
		return Part{}, err
	}
	if partNumber < 1 || partNumber > limits.MaxMultipartParts {
		return Part{}, fmt.Errorf("multipartstage: part number %d: %w", partNumber, errs.ErrInvalidPart)
	}
	if size < 0 || body == nil {
		return Part{}, fmt.Errorf("multipartstage: part %d: %w: declared size and body are required", partNumber, errs.ErrInvalidPart)
	}
	upload, err := s.deps.Index.GetUpload(ctx, uploadID)
	if err != nil {
		return Part{}, fmt.Errorf("multipartstage: put part: %w", err)
	}
	if err := requireOpen(upload); err != nil {
		return Part{}, err
	}
	if err := s.checkRunningTotal(ctx, upload, partNumber, size); err != nil {
		return Part{}, err
	}
	return s.stagePart(ctx, upload, partNumber, size, body)
}

func requireOpen(upload jobrepo.MultipartUpload) error {
	switch upload.State {
	case jobrepo.MultipartOpen:
		return nil
	case jobrepo.MultipartCommitting:
		return fmt.Errorf("multipartstage: upload %s: %w", upload.UploadID, errs.ErrUploadBusy)
	}
	return fmt.Errorf("multipartstage: upload %s: %w", upload.UploadID, errs.ErrNoSuchUpload)
}

func (s *Stage) checkRunningTotal(ctx context.Context, upload jobrepo.MultipartUpload, partNumber int, size int64) error {
	if size > limits.MaxStoredObjectBytes {
		return fmt.Errorf("multipartstage: part %d of %d bytes: %w", partNumber, size, errs.ErrObjectTooLarge)
	}
	staged, err := s.listAllParts(ctx, upload.UploadID)
	if err != nil {
		return err
	}
	total := size
	for _, part := range staged {
		if part.PartNumber != partNumber {
			total += part.Size
		}
	}
	if exceedsStoredCeiling(upload.Encrypted, total) {
		return fmt.Errorf("multipartstage: part %d: staged total would exceed the stored size ceiling: %w", partNumber, errs.ErrObjectTooLarge)
	}
	return nil
}

func (s *Stage) stagePart(ctx context.Context, upload jobrepo.MultipartUpload, partNumber int, size int64, body io.Reader) (Part, error) {
	staged, err := s.writeTemp(ctx, upload.UploadID, size, body)
	if err != nil {
		return Part{}, err
	}
	row := jobrepo.MultipartPart{
		UploadID:   upload.UploadID,
		PartNumber: partNumber,
		Size:       size,
		MD5:        staged.md5Hex,
		StagedAt:   s.deps.Clock.Now().UnixMilli(),
	}
	if err := s.deps.Index.PutPart(ctx, row); err != nil {
		s.discardTemp(staged.tempPath)
		if errors.Is(err, errs.ErrNoSuchUpload) {
			_ = s.removeUploadFiles(upload.UploadID)
		}
		return Part{}, fmt.Errorf("multipartstage: record part %d: %w", partNumber, err)
	}
	if err := s.publishPart(upload.UploadID, partNumber, staged.tempPath); err != nil {
		s.discardTemp(staged.tempPath)
		return Part{}, err
	}
	return partFromRow(row), nil
}

func (s *Stage) writeTemp(ctx context.Context, uploadID string, size int64, body io.Reader) (staged stagedFile, retErr error) {
	dir := s.uploadDir(uploadID)
	if err := s.deps.FS.MkdirAll(dir, uploadDirMode); err != nil {
		return stagedFile{}, fmt.Errorf("multipartstage: create upload directory: %w", err)
	}
	file, err := s.deps.FS.CreateTemp(dir, partTempPattern)
	if err != nil {
		return stagedFile{}, fmt.Errorf("multipartstage: create part temp file: %w", err)
	}
	tempPath := file.Name()
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
		if retErr != nil {
			s.discardTemp(tempPath)
		}
	}()
	if err := file.Chmod(partFileMode); err != nil {
		return stagedFile{}, fmt.Errorf("multipartstage: chmod part temp file: %w", err)
	}
	digest := md5.New()
	if err := copyExactly(file, digest, contextReader{ctx: ctx, src: body}, size); err != nil {
		return stagedFile{}, err
	}
	if err := file.Sync(); err != nil {
		return stagedFile{}, fmt.Errorf("multipartstage: sync part temp file: %w", err)
	}
	closed = true
	if err := file.Close(); err != nil {
		return stagedFile{}, fmt.Errorf("multipartstage: close part temp file: %w", err)
	}
	return stagedFile{tempPath: tempPath, md5Hex: hex.EncodeToString(digest.Sum(nil))}, nil
}

func copyExactly(dst io.Writer, digest hash.Hash, src io.Reader, size int64) error {
	written, err := io.Copy(io.MultiWriter(dst, digest), io.LimitReader(src, size+1))
	if err != nil {
		return fmt.Errorf("multipartstage: stream part: %w", err)
	}
	if written < size {
		return fmt.Errorf("multipartstage: part body of %d bytes, declared %d: %w", written, size, io.ErrUnexpectedEOF)
	}
	if written > size {
		return fmt.Errorf("multipartstage: part body exceeds the declared %d bytes: %w", size, errs.ErrInvalidPart)
	}
	return nil
}

func (s *Stage) publishPart(uploadID string, partNumber int, tempPath string) error {
	if err := s.deps.FS.Rename(tempPath, s.partPath(uploadID, partNumber)); err != nil {
		return fmt.Errorf("multipartstage: publish part %d: %w", partNumber, err)
	}
	if err := s.deps.FS.SyncDir(s.uploadDir(uploadID)); err != nil {
		return fmt.Errorf("multipartstage: sync upload directory: %w", err)
	}
	return nil
}

func (s *Stage) discardTemp(path string) {
	_ = s.deps.FS.Remove(path)
}
