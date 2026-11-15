package put

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/PRIEYAN/ButterDB/internal/crypto/format"
	"github.com/PRIEYAN/ButterDB/internal/crypto/stream"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/storage/jobrepo"
)

func (s *Putter) encryptionParamsFor(ctx context.Context, res resolved) (*encryptionParams, error) {
	if !res.encrypted {
		return nil, nil
	}
	if err := s.deps.Authority.EnsureKey(ctx, res.info.ChannelID); err != nil {
		return nil, fmt.Errorf("put: ensure bucket key: %w", err)
	}
	key, found, err := s.deps.Keys.ActiveKey(ctx, res.info.ChannelID, s.deps.Config.KEKFingerprint)
	if err != nil {
		return nil, fmt.Errorf("put: look up active key: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("put: bucket %d has no active key: %w", res.info.ChannelID, errs.ErrKeyUnavailable)
	}
	params, err := stream.NewFileParams(s.deps.Random)
	if err != nil {
		return nil, fmt.Errorf("put: draw file parameters: %w", err)
	}
	return &encryptionParams{
		keyID:       key.KeyID,
		fileSalt:    append([]byte(nil), params.Salt[:]...),
		noncePrefix: append([]byte(nil), params.NoncePrefix[:]...),
	}, nil
}

func fileParamsOf(job jobrepo.UploadJob) (salt [format.SaltLen]byte, prefix [format.NoncePrefixLen]byte, err error) {
	if len(job.FileSalt) != format.SaltLen || len(job.NoncePrefix) != format.NoncePrefixLen {
		return salt, prefix, fmt.Errorf("put: job %s: encryption parameters are missing: %w", job.JobID, jobrepo.ErrCorruptRow)
	}
	copy(salt[:], job.FileSalt)
	copy(prefix[:], job.NoncePrefix)
	return salt, prefix, nil
}

func (s *Putter) openEncrypted(ctx context.Context, job jobrepo.UploadJob, plain io.ReadSeekCloser) (io.ReadSeeker, func(), error) {
	salt, prefix, err := fileParamsOf(job)
	if err != nil {
		_ = plain.Close()
		return nil, nil, err
	}
	key, found, err := s.deps.Keys.Key(ctx, job.BucketChannelID, job.KeyID)
	if err != nil {
		_ = plain.Close()
		return nil, nil, fmt.Errorf("put: job %s: look up key: %w", job.JobID, err)
	}
	if !found {
		_ = plain.Close()
		return nil, nil, fmt.Errorf("put: job %s: key %s: %w", job.JobID, job.KeyID, errs.ErrKeyUnavailable)
	}
	dek, err := s.deps.Unwrap.UnwrapDEK(key.WrappedDEK, key.KeyCheck, job.BucketChannelID, key.KeyID)
	if err != nil {
		_ = plain.Close()
		return nil, nil, fmt.Errorf("put: job %s: unwrap key: %w: %w", job.JobID, errs.ErrKeyUnavailable, err)
	}
	defer dek.Clear()
	sealed, err := stream.NewEncryptingReaderAt(readerAtOf(plain), job.PlaintextSize, dek.Bytes(), salt, prefix)
	if err != nil {
		_ = plain.Close()
		return nil, nil, fmt.Errorf("put: job %s: start encryption: %w", job.JobID, err)
	}
	if sealed.Size() != job.StoredSize {
		_ = sealed.Close()
		_ = plain.Close()
		return nil, nil, fmt.Errorf("put: job %s: sealed size %d differs from planned %d: %w", job.JobID, sealed.Size(), job.StoredSize, jobrepo.ErrCorruptRow)
	}
	closeAll := func() {
		_ = sealed.Close()
		_ = plain.Close()
	}
	return io.NewSectionReader(sealed, 0, sealed.Size()), closeAll, nil
}

type seekingReaderAt struct {
	mu   sync.Mutex
	file io.ReadSeeker
}

func (r *seekingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.file.Seek(off, io.SeekStart); err != nil {
		return 0, err
	}
	return io.ReadFull(r.file, p)
}

func readerAtOf(file io.ReadSeeker) io.ReaderAt {
	if at, ok := file.(io.ReaderAt); ok {
		return at
	}
	return &seekingReaderAt{file: file}
}
