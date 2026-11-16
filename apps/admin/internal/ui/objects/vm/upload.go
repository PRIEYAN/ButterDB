package vm

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

const (
	EncryptionBucketDefault = ""
	EncryptionOn            = "tde1"
	EncryptionOff           = "none"

	sniffBytes             = 512
	maxCommitUnknownRetry  = 5
	minCommitRetryDelay    = 5 * time.Second
	maxFloodWaitRetry      = 5
	floodWaitFallbackDelay = 5 * time.Second
	maxPreconditionRounds  = 5
	defaultMaxKeyBytes     = 1024
	defaultMaxMetaBytes    = 2048
)

type ReplaceChoice int

const (
	ReplaceOnce ReplaceChoice = iota
	SkipOnce
	ReplaceAll
	SkipAll
)

type ReplaceAsker func(ctx context.Context, key string, existing adminv1.ObjectMeta) ReplaceChoice

type ConflictChoice int

const (
	ConflictReplace ConflictChoice = iota
	ConflictKeepBoth
	ConflictSkip
)

type ConflictAsker func(ctx context.Context, path string) ConflictChoice

type Prompts struct {
	Replace  ReplaceAsker
	Conflict ConflictAsker
}

type uploadBatch struct {
	prompts Prompts
	mu      sync.Mutex
	sticky  *ReplaceChoice
}

func (v *ViewModel) PrepareUpload(prefix, path string) (UploadSpec, error) {
	f, err := v.fs.Open(path)
	if err != nil {
		return UploadSpec{}, err
	}
	defer f.Close()
	head := make([]byte, sniffBytes)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return UploadSpec{}, err
	}
	size, err := fileSize(f)
	if err != nil {
		return UploadSpec{}, err
	}
	name := filepath.Base(path)
	return UploadSpec{
		Path:        path,
		Key:         prefix + name,
		Size:        size,
		ContentType: detectContentType(name, head[:n]),
	}, nil
}

func detectContentType(name string, head []byte) string {
	if byExtension := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); byExtension != "" {
		return byExtension
	}
	return http.DetectContentType(head)
}

func (v *ViewModel) Upload(ctx context.Context, bucket string, specs []UploadSpec, prompts Prompts) {
	batch := &uploadBatch{prompts: prompts}
	jobs := v.enqueueUploads(bucket, specs)
	var wg sync.WaitGroup
	for _, j := range jobs {
		if !v.acquireSlot(ctx) {
			v.cancelQueued(j)
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer v.releaseSlot()
			v.runUpload(ctx, j, batch)
		}()
	}
	wg.Wait()
}

func (v *ViewModel) enqueueUploads(bucket string, specs []UploadSpec) []*job {
	v.tmu.Lock()
	defer v.tmu.Unlock()
	jobs := make([]*job, 0, len(specs))
	for _, spec := range specs {
		key, err := v.newIdempotencyKey()
		j := &job{spec: spec, idemKey: key}
		j.Kind, j.Bucket, j.Key, j.Path, j.Total = KindUpload, bucket, spec.Key, spec.Path, spec.Size
		v.addJobLocked(j)
		if err != nil {
			j.State = TransferFailed
			j.Problem = v.problemFor(err)
		}
		jobs = append(jobs, j)
	}
	v.publishTransfersLocked()
	return jobs
}

func (v *ViewModel) cancelQueued(j *job) {
	v.updateJob(j, func(j *job) {
		if j.State == TransferQueued {
			j.State = TransferCancelled
		}
	})
}

func (v *ViewModel) RetryUpload(ctx context.Context, id int, prompts Prompts) bool {
	j := v.findRetryable(id, KindUpload)
	if j == nil {
		return false
	}
	if !v.acquireSlot(ctx) {
		v.cancelQueued(j)
		return false
	}
	defer v.releaseSlot()
	v.runUpload(ctx, j, &uploadBatch{prompts: prompts})
	return true
}

func (v *ViewModel) findRetryable(id int, kind TransferKind) *job {
	v.tmu.Lock()
	defer v.tmu.Unlock()
	for _, j := range v.jobs {
		if j.ID != id || j.Kind != kind || j.running {
			continue
		}
		if j.State != TransferFailed && j.State != TransferCancelled {
			return nil
		}
		j.State = TransferQueued
		j.Problem = nil
		v.publishTransfersLocked()
		return j
	}
	return nil
}

func (v *ViewModel) runUpload(parent context.Context, j *job, batch *uploadBatch) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	if !v.beginJob(j, cancel) {
		return
	}
	spec, bucket := j.spec, j.Bucket
	size, err := v.measureUpload(spec)
	if err != nil {
		v.failUpload(ctx, j, err)
		return
	}
	v.updateJob(j, func(j *job) { j.Total = size })
	limits, err := v.ensureLimits(ctx)
	if err != nil {
		v.failUpload(ctx, j, err)
		return
	}
	if problem := precheckUpload(spec, size, limits); problem != nil {
		v.finishJob(j, TransferFailed, problem)
		return
	}
	reask := false
	for round := 0; round < maxPreconditionRounds; round++ {
		opts, outcome, err := v.resolvePrecondition(ctx, bucket, spec, batch, reask)
		if err != nil {
			v.failUpload(ctx, j, err)
			return
		}
		if outcome == TransferSkipped {
			v.finishJob(j, TransferSkipped, nil)
			return
		}
		opts.ContentType = spec.ContentType
		opts.Encryption = spec.Encryption
		opts.UserMetadata = spec.Metadata
		meta, err := v.putWithRetries(ctx, j, opts, size)
		if err == nil {
			v.updateJob(j, func(j *job) { j.ETag = meta.ETag })
			v.finishJob(j, TransferDone, nil)
			return
		}
		if adminv1.IsCode(err, adminv1.CodePreconditionFailed) {
			reask = true
			continue
		}
		v.failUpload(ctx, j, err)
		return
	}
	v.failUpload(ctx, j, syntheticAPIError(http.StatusPreconditionFailed, adminv1.CodePreconditionFailed, nil))
}

func (v *ViewModel) measureUpload(spec UploadSpec) (int64, error) {
	f, err := v.fs.Open(spec.Path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return fileSize(f)
}

func precheckUpload(spec UploadSpec, size int64, limits adminv1.Limits) *model.Problem {
	maxKey := limits.MaxKeyBytes
	if maxKey <= 0 {
		maxKey = defaultMaxKeyBytes
	}
	if spec.Key == "" || len(spec.Key) > maxKey || !utf8.ValidString(spec.Key) {
		p := model.Describe(syntheticAPIError(http.StatusBadRequest, adminv1.CodeObjectKeyInvalid, nil), time.Time{})
		return &p
	}
	maxMeta := limits.MaxUserMetadataBytes
	if maxMeta <= 0 {
		maxMeta = defaultMaxMetaBytes
	}
	if metadataBytes(spec.Metadata) > maxMeta {
		p := model.Describe(syntheticAPIError(http.StatusRequestEntityTooLarge, adminv1.CodeMetadataTooLarge, map[string]any{"max_bytes": maxMeta}), time.Time{})
		return &p
	}
	if limits.MaxObjectBytes > 0 && size > limits.MaxObjectBytes {
		p := model.Describe(syntheticAPIError(http.StatusRequestEntityTooLarge, adminv1.CodeObjectTooLarge, map[string]any{"max_bytes": limits.MaxObjectBytes}), time.Time{})
		return &p
	}
	return nil
}

func metadataBytes(metadata map[string]string) int {
	total := 0
	for name, value := range metadata {
		total += len(name) + len(value)
	}
	return total
}

func (v *ViewModel) resolvePrecondition(ctx context.Context, bucket string, spec UploadSpec, batch *uploadBatch, reask bool) (adminv1.PutObjectOptions, TransferState, error) {
	existing, err := v.client.HeadObject(ctx, bucket, spec.Key)
	if isAbsent(err) {
		return adminv1.PutObjectOptions{IfNoneMatchAny: true}, TransferSending, nil
	}
	if err != nil {
		return adminv1.PutObjectOptions{}, "", err
	}
	if batch.decideReplace(ctx, spec.Key, existing, reask) {
		return adminv1.PutObjectOptions{IfMatch: existing.ETag}, TransferSending, nil
	}
	return adminv1.PutObjectOptions{}, TransferSkipped, nil
}

func (b *uploadBatch) decideReplace(ctx context.Context, key string, existing adminv1.ObjectMeta, reask bool) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sticky != nil && !reask {
		return *b.sticky == ReplaceAll
	}
	if b.prompts.Replace == nil {
		return false
	}
	choice := b.prompts.Replace(ctx, key, existing)
	switch choice {
	case ReplaceAll, SkipAll:
		sticky := choice
		b.sticky = &sticky
	}
	return choice == ReplaceOnce || choice == ReplaceAll
}

func (v *ViewModel) putWithRetries(ctx context.Context, j *job, opts adminv1.PutObjectOptions, size int64) (adminv1.ObjectMeta, error) {
	unknownRetries, floodRetries := 0, 0
	for {
		if err := v.waitPause(ctx); err != nil {
			return adminv1.ObjectMeta{}, err
		}
		v.tmu.Lock()
		opts.IdempotencyKey = j.idemKey
		v.tmu.Unlock()
		meta, err := v.putOnce(ctx, j, opts, size)
		if err == nil || ctx.Err() != nil {
			return meta, err
		}
		switch {
		case adminv1.IsCode(err, adminv1.CodeCommitOutcomeUnknown) && unknownRetries < maxCommitUnknownRetry:
			unknownRetries++
			if err := v.waitCommitRecheck(ctx, j, err); err != nil {
				return meta, err
			}
		case adminv1.IsCode(err, adminv1.CodeTelegramFloodWait) && floodRetries < maxFloodWaitRetry:
			floodRetries++
			v.setPause(v.floodWaitUntil(err))
		default:
			return meta, err
		}
	}
}

func (v *ViewModel) waitCommitRecheck(ctx context.Context, j *job, err error) error {
	problem := v.problemFor(err)
	v.updateJob(j, func(j *job) {
		j.State = TransferCommitting
		if problem != nil {
			j.Message = problem.Title
		}
	})
	delay := minCommitRetryDelay
	if after, ok := adminv1.RetryAfter(err); ok && after > delay {
		delay = after
	}
	return v.sleep(ctx, delay)
}

func (v *ViewModel) floodWaitUntil(err error) time.Time {
	problem := model.Describe(err, v.now())
	if problem.RetryAt.After(v.now()) {
		return problem.RetryAt
	}
	return v.now().Add(floodWaitFallbackDelay)
}

func (v *ViewModel) putOnce(ctx context.Context, j *job, opts adminv1.PutObjectOptions, size int64) (adminv1.ObjectMeta, error) {
	f, err := v.fs.Open(j.spec.Path)
	if err != nil {
		return adminv1.ObjectMeta{}, err
	}
	defer f.Close()
	v.updateJob(j, func(j *job) {
		j.State = TransferSending
		j.Sent = 0
		j.Message = ""
	})
	if size == 0 {
		v.markCommitting(j)
	}
	body := &countingReader{src: io.LimitReader(f, size), onRead: func(total int64) {
		v.reportProgress(j, total)
		if total >= size {
			v.markCommitting(j)
		}
	}}
	return v.client.PutObject(ctx, j.Bucket, j.Key, body, size, opts)
}

func (v *ViewModel) failUpload(ctx context.Context, j *job, err error) {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		v.finishJob(j, TransferCancelled, nil)
		return
	}
	if adminv1.IsCode(err, adminv1.CodeIdempotencyConflict) {
		if key, keyErr := v.newIdempotencyKey(); keyErr == nil {
			v.updateJob(j, func(j *job) { j.idemKey = key })
		}
	}
	v.finishJob(j, TransferFailed, v.problemFor(err))
}

func ParseMetadata(text string) (map[string]string, *model.Problem) {
	out := make(map[string]string)
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		name = strings.ToLower(strings.TrimSpace(name))
		if !ok || name == "" {
			return nil, validationProblem("metadata", "write each pair as name=value")
		}
		out[name] = strings.TrimSpace(value)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}
