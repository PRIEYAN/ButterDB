package vm

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"path/filepath"
	"strings"
	"sync"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

const (
	partFilePrefix    = "."
	partFileSuffix    = ".butterdb-part"
	md5HexLength      = 32
	multipartETagMark = "-"
	unsafeSegment     = "_"
)

var (
	ErrDownloadIncomplete = errors.New("objects: download ended before the full object arrived")
	ErrDownloadChecksum   = errors.New("objects: download does not match the object checksum")
)

type DownloadRequest struct {
	Key        string
	TargetPath string
}

func SafeLocalPath(folder, prefix, key string, separator rune) string {
	relative := strings.TrimPrefix(key, prefix)
	if relative == "" {
		relative = key
	}
	segments := make([]string, 0, 4)
	for _, segment := range strings.Split(relative, "/") {
		if segment == "" {
			continue
		}
		segments = append(segments, safeSegment(segment, separator))
	}
	if len(segments) == 0 {
		segments = append(segments, unsafeSegment)
	}
	return filepath.Join(append([]string{folder}, segments...)...)
}

func safeSegment(segment string, separator rune) string {
	if segment == "." || segment == ".." {
		return unsafeSegment
	}
	if strings.ContainsRune(segment, separator) || strings.ContainsRune(segment, 0) {
		return unsafeSegment
	}
	return segment
}

func FolderTargets(folder, prefix string, keys []string) []DownloadRequest {
	out := make([]DownloadRequest, 0, len(keys))
	for _, key := range keys {
		out = append(out, DownloadRequest{Key: key, TargetPath: SafeLocalPath(folder, prefix, key, filepath.Separator)})
	}
	return out
}

func DefaultFileName(key string) string {
	last := key[strings.LastIndex(key, "/")+1:]
	if last == "" {
		return "download"
	}
	return safeSegment(last, filepath.Separator)
}

func PartFilePath(target string) string {
	return filepath.Join(filepath.Dir(target), partFilePrefix+filepath.Base(target)+partFileSuffix)
}

func KeepBothPath(target string, exists func(string) bool) string {
	dir := filepath.Dir(target)
	ext := filepath.Ext(target)
	stem := strings.TrimSuffix(filepath.Base(target), ext)
	for n := 1; ; n++ {
		candidate := filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, n, ext))
		if !exists(candidate) {
			return candidate
		}
	}
}

func (v *ViewModel) Download(ctx context.Context, bucket string, requests []DownloadRequest, prompts Prompts) {
	jobs := v.enqueueDownloads(bucket, requests)
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
			v.runDownload(ctx, j, prompts)
		}()
	}
	wg.Wait()
}

func (v *ViewModel) enqueueDownloads(bucket string, requests []DownloadRequest) []*job {
	v.tmu.Lock()
	defer v.tmu.Unlock()
	jobs := make([]*job, 0, len(requests))
	for _, req := range requests {
		j := &job{target: req.TargetPath}
		j.Kind, j.Bucket, j.Key, j.Path, j.Total = KindDownload, bucket, req.Key, req.TargetPath, -1
		v.addJobLocked(j)
		jobs = append(jobs, j)
	}
	v.publishTransfersLocked()
	return jobs
}

func (v *ViewModel) RetryDownload(ctx context.Context, id int, prompts Prompts) bool {
	j := v.findRetryable(id, KindDownload)
	if j == nil {
		return false
	}
	if !v.acquireSlot(ctx) {
		v.cancelQueued(j)
		return false
	}
	defer v.releaseSlot()
	v.runDownload(ctx, j, prompts)
	return true
}

func (v *ViewModel) runDownload(parent context.Context, j *job, prompts Prompts) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	if !v.beginJob(j, cancel) {
		return
	}
	target, skip := v.resolveTarget(ctx, j.target, prompts)
	if skip {
		v.finishJob(j, TransferSkipped, nil)
		return
	}
	if err := v.fetchToTarget(ctx, j, target); err != nil {
		v.failDownload(ctx, j, err)
		return
	}
	v.finishJob(j, TransferDone, nil)
}

func (v *ViewModel) resolveTarget(ctx context.Context, target string, prompts Prompts) (string, bool) {
	if !v.fs.Exists(target) || prompts.Conflict == nil {
		return target, false
	}
	switch prompts.Conflict(ctx, target) {
	case ConflictKeepBoth:
		return KeepBothPath(target, v.fs.Exists), false
	case ConflictSkip:
		return "", true
	}
	return target, false
}

func (v *ViewModel) fetchToTarget(ctx context.Context, j *job, target string) error {
	stream, err := v.client.GetObject(ctx, j.Bucket, j.Key, adminv1.GetObjectOptions{})
	if err != nil {
		return err
	}
	defer stream.Body.Close()
	total := stream.ContentLength
	if total < 0 {
		total = stream.Meta.SizeBytes
	}
	v.updateJob(j, func(j *job) { j.Total = total; j.Path = target })
	if err := v.fs.MkdirAll(filepath.Dir(target)); err != nil {
		return err
	}
	part := PartFilePath(target)
	written, sum, err := v.writePart(ctx, j, stream, part)
	if err != nil {
		_ = v.fs.Remove(part)
		return err
	}
	if err := verifyDownload(stream, total, written, sum); err != nil {
		_ = v.fs.Remove(part)
		return err
	}
	if err := v.fs.Rename(part, target); err != nil {
		_ = v.fs.Remove(part)
		return err
	}
	return nil
}

func (v *ViewModel) writePart(ctx context.Context, j *job, stream adminv1.ObjectStream, part string) (int64, hash.Hash, error) {
	out, err := v.fs.Create(part)
	if err != nil {
		return 0, nil, err
	}
	sum := md5.New()
	progress := &countingWriter{dst: io.MultiWriter(out, sum), onWrite: func(total int64) { v.reportProgress(j, total) }}
	written, copyErr := io.Copy(progress, &contextReader{ctx: ctx, src: stream.Body})
	closeErr := out.Close()
	if copyErr != nil {
		return written, sum, copyErr
	}
	if closeErr != nil {
		return written, sum, closeErr
	}
	return written, sum, nil
}

func verifyDownload(stream adminv1.ObjectStream, total, written int64, sum hash.Hash) error {
	if total >= 0 && written != total {
		return ErrDownloadIncomplete
	}
	etag := strings.Trim(stream.Meta.ETag, `"`)
	if stream.Status != 200 || !isPlainMD5ETag(etag) {
		return nil
	}
	if !strings.EqualFold(etag, hex.EncodeToString(sum.Sum(nil))) {
		return ErrDownloadChecksum
	}
	return nil
}

func isPlainMD5ETag(etag string) bool {
	if len(etag) != md5HexLength || strings.Contains(etag, multipartETagMark) {
		return false
	}
	_, err := hex.DecodeString(etag)
	return err == nil
}

func (v *ViewModel) failDownload(ctx context.Context, j *job, err error) {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		v.finishJob(j, TransferCancelled, nil)
		return
	}
	v.finishJob(j, TransferFailed, v.describeTransferError(err))
}

func (v *ViewModel) describeTransferError(err error) *model.Problem {
	p := model.Describe(err, v.now())
	switch {
	case errors.Is(err, ErrDownloadIncomplete):
		p.Body = "The download ended early. Try again."
	case errors.Is(err, ErrDownloadChecksum):
		p.Body = "The downloaded bytes do not match the object checksum. The file was discarded."
	}
	return &p
}

type countingWriter struct {
	dst     io.Writer
	total   int64
	onWrite func(total int64)
}

func (w *countingWriter) Write(p []byte) (int, error) {
	n, err := w.dst.Write(p)
	if n > 0 {
		w.total += int64(n)
		w.onWrite(w.total)
	}
	return n, err
}

type contextReader struct {
	ctx context.Context
	src io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.src.Read(p)
}
