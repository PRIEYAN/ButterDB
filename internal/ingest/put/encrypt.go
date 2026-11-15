package put

import (
	"context"
	"fmt"
	"io"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/storage/jobrepo"
)

func (s *Putter) openEncrypted(_ context.Context, job jobrepo.UploadJob, _ io.ReadSeekCloser) (io.ReadSeeker, func(), error) {
	return nil, nil, fmt.Errorf("put: job %s: encrypted bodies: %w", job.JobID, errs.ErrNotImplemented)
}
