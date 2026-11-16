package vm

import (
	"context"
	"time"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

type TransferKind string

const (
	KindUpload   TransferKind = "upload"
	KindDownload TransferKind = "download"
)

type TransferState string

const (
	TransferQueued     TransferState = "queued"
	TransferSending    TransferState = "sending"
	TransferCommitting TransferState = "committing"
	TransferDone       TransferState = "done"
	TransferFailed     TransferState = "failed"
	TransferCancelled  TransferState = "cancelled"
	TransferSkipped    TransferState = "skipped"
)

type Transfer struct {
	ID             int
	Kind           TransferKind
	Bucket         string
	Key            string
	Path           string
	State          TransferState
	Sent           int64
	Total          int64
	BytesPerSecond float64
	ETag           string
	Message        string
	Problem        *model.Problem
	Cancellable    bool
}

type TransferList struct {
	Items       []Transfer
	PausedUntil time.Time
	Active      int
}

type UploadSpec struct {
	Path        string
	Key         string
	Size        int64
	ContentType string
	Encryption  string
	Metadata    map[string]string
}

type job struct {
	Transfer
	started     time.Time
	lastPublish time.Time
	cancel      context.CancelFunc
	spec        UploadSpec
	idemKey     string
	target      string
	running     bool
}

func (j *job) active() bool {
	switch j.State {
	case TransferQueued, TransferSending, TransferCommitting:
		return true
	}
	return false
}

func (v *ViewModel) publishTransfersLocked() {
	items := make([]Transfer, 0, len(v.jobs))
	active := 0
	for _, j := range v.jobs {
		item := j.Transfer
		item.Cancellable = j.State == TransferQueued || j.State == TransferSending
		if j.active() {
			active++
		}
		items = append(items, item)
	}
	v.transfers.Set(TransferList{Items: items, PausedUntil: v.pausedUntil, Active: active})
}

func (v *ViewModel) addJobLocked(j *job) *job {
	v.nextJobID++
	j.ID = v.nextJobID
	j.State = TransferQueued
	v.jobs = append(v.jobs, j)
	return j
}

func (v *ViewModel) updateJob(j *job, fn func(*job)) {
	v.tmu.Lock()
	defer v.tmu.Unlock()
	fn(j)
	v.publishTransfersLocked()
}

func (v *ViewModel) beginJob(j *job, cancel context.CancelFunc) bool {
	v.tmu.Lock()
	defer v.tmu.Unlock()
	if j.State != TransferQueued {
		return false
	}
	j.cancel = cancel
	j.running = true
	j.State = TransferSending
	j.Sent = 0
	j.Problem = nil
	j.Message = ""
	j.started = v.now()
	j.lastPublish = time.Time{}
	v.publishTransfersLocked()
	return true
}

func (v *ViewModel) reportProgress(j *job, sent int64) {
	v.tmu.Lock()
	defer v.tmu.Unlock()
	j.Sent = sent
	now := v.now()
	if !j.lastPublish.IsZero() && now.Sub(j.lastPublish) < progressInterval {
		return
	}
	j.lastPublish = now
	if elapsed := now.Sub(j.started); elapsed > 0 {
		j.BytesPerSecond = float64(sent) / elapsed.Seconds()
	}
	v.publishTransfersLocked()
}

func (v *ViewModel) markCommitting(j *job) {
	v.updateJob(j, func(j *job) {
		if j.State == TransferSending {
			j.State = TransferCommitting
			j.Sent = j.Total
			j.Message = ""
		}
	})
}

func (v *ViewModel) finishJob(j *job, state TransferState, problem *model.Problem) {
	v.updateJob(j, func(j *job) {
		j.State = state
		j.Problem = problem
		j.running = false
		j.cancel = nil
		j.Message = ""
		j.BytesPerSecond = 0
	})
}

func (v *ViewModel) Cancel(id int) bool {
	v.tmu.Lock()
	defer v.tmu.Unlock()
	for _, j := range v.jobs {
		if j.ID != id {
			continue
		}
		switch j.State {
		case TransferQueued:
			j.State = TransferCancelled
		case TransferSending:
			if j.cancel != nil {
				j.cancel()
			}
		default:
			return false
		}
		v.publishTransfersLocked()
		return true
	}
	return false
}

func (v *ViewModel) Remove(id int) bool {
	v.tmu.Lock()
	defer v.tmu.Unlock()
	for i, j := range v.jobs {
		if j.ID != id || j.active() || j.running {
			continue
		}
		v.jobs = append(v.jobs[:i], v.jobs[i+1:]...)
		v.publishTransfersLocked()
		return true
	}
	return false
}

func (v *ViewModel) ClearFinished() {
	v.tmu.Lock()
	defer v.tmu.Unlock()
	kept := v.jobs[:0]
	for _, j := range v.jobs {
		if j.active() || j.running {
			kept = append(kept, j)
		}
	}
	v.jobs = kept
	v.publishTransfersLocked()
}

func (v *ViewModel) ActiveTransfers() int {
	v.tmu.Lock()
	defer v.tmu.Unlock()
	active := 0
	for _, j := range v.jobs {
		if j.active() {
			active++
		}
	}
	return active
}

func (v *ViewModel) acquireSlot(ctx context.Context) bool {
	select {
	case v.slots <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (v *ViewModel) releaseSlot() {
	<-v.slots
}

func (v *ViewModel) setPause(until time.Time) {
	v.tmu.Lock()
	defer v.tmu.Unlock()
	if until.After(v.pausedUntil) {
		v.pausedUntil = until
		v.publishTransfersLocked()
	}
}

func (v *ViewModel) waitPause(ctx context.Context) error {
	for {
		v.tmu.Lock()
		until := v.pausedUntil
		v.tmu.Unlock()
		wait := until.Sub(v.now())
		if until.IsZero() || wait <= 0 {
			v.clearPause(until)
			return ctx.Err()
		}
		if err := v.sleep(ctx, wait); err != nil {
			return err
		}
		v.clearPause(until)
	}
}

func (v *ViewModel) clearPause(until time.Time) {
	v.tmu.Lock()
	defer v.tmu.Unlock()
	if v.pausedUntil.Equal(until) && !until.IsZero() {
		v.pausedUntil = time.Time{}
		v.publishTransfersLocked()
	}
}

type countingReader struct {
	src    interface{ Read([]byte) (int, error) }
	onRead func(total int64)
	total  int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.src.Read(p)
	if n > 0 {
		r.total += int64(n)
		r.onRead(r.total)
	}
	return n, err
}
