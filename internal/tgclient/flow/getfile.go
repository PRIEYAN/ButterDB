package flow

import (
	"context"
	"sync"
)

const (
	MaxConcurrentGetFile           = 12
	ForegroundGetFileReserve       = 4
	MaxConcurrentBackgroundGetFile = MaxConcurrentGetFile - ForegroundGetFileReserve
	DefaultDownloadThreads         = 4
	UploadThreads                  = 8
)

type GetFileLimiter struct {
	global     *weightedSemaphore
	background *weightedSemaphore
}

func NewGetFileLimiter() *GetFileLimiter {
	return &GetFileLimiter{
		global:     newWeightedSemaphore(MaxConcurrentGetFile),
		background: newWeightedSemaphore(MaxConcurrentBackgroundGetFile),
	}
}

func (l *GetFileLimiter) AcquireForeground(ctx context.Context, n int) (func(), error) {
	weight := clampGetFileWeight(n, MaxConcurrentGetFile)
	if err := l.global.acquire(ctx, weight); err != nil {
		return nil, err
	}
	return onceRelease(func() { l.global.release(weight) }), nil
}

func (l *GetFileLimiter) TryAcquireBackground(n int) (func(), bool) {
	weight := clampGetFileWeight(n, MaxConcurrentBackgroundGetFile)
	if !l.background.tryAcquire(weight) {
		return nil, false
	}
	if !l.global.tryAcquire(weight) {
		l.background.release(weight)
		return nil, false
	}
	return onceRelease(func() {
		l.global.release(weight)
		l.background.release(weight)
	}), true
}

type UploadPartSlots struct {
	slots *weightedSemaphore
}

func NewUploadPartSlots() *UploadPartSlots {
	return &UploadPartSlots{slots: newWeightedSemaphore(UploadThreads)}
}

func (u *UploadPartSlots) Acquire(ctx context.Context) (func(), error) {
	if err := u.slots.acquire(ctx, 1); err != nil {
		return nil, err
	}
	return onceRelease(func() { u.slots.release(1) }), nil
}

func onceRelease(release func()) func() {
	var once sync.Once
	return func() { once.Do(release) }
}

func clampGetFileWeight(n, maxWeight int) int {
	if n < 1 {
		return 1
	}
	if n > maxWeight {
		return maxWeight
	}
	return n
}
