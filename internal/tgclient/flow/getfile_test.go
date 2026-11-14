package flow

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
)

func TestBackgroundLimiterLeavesForegroundHeadroom(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		limiter := NewGetFileLimiter()

		releaseA, ok := limiter.TryAcquireBackground(DefaultDownloadThreads)
		if !ok {
			t.Fatal("first download refused")
		}
		defer releaseA()
		releaseB, ok := limiter.TryAcquireBackground(DefaultDownloadThreads)
		if !ok {
			t.Fatal("second download refused")
		}
		defer releaseB()

		if release, ok := limiter.TryAcquireBackground(DefaultDownloadThreads); ok {
			release()
			t.Fatal("third download acquired despite a full background pool")
		}

		for i := 0; i < ForegroundGetFileReserve; i++ {
			release, err := limiter.AcquireForeground(ctx, 1)
			if err != nil {
				t.Fatalf("foreground read %d refused while background is full: %v", i, err)
			}
			defer release()
		}
	})
}

func TestBackgroundNeverBlocksForeground(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		limiter := NewGetFileLimiter()

		releaseDownload, ok := limiter.TryAcquireBackground(DefaultDownloadThreads)
		if !ok {
			t.Fatal("download refused")
		}
		defer releaseDownload()

		var foreground []func()
		for i := 0; i < MaxConcurrentGetFile-DefaultDownloadThreads; i++ {
			release, err := limiter.AcquireForeground(ctx, 1)
			if err != nil {
				t.Fatalf("foreground fill slot %d: %v", i, err)
			}
			foreground = append(foreground, release)
		}

		if release, ok := limiter.TryAcquireBackground(DefaultDownloadThreads); ok {
			release()
			t.Fatal("second download acquired despite a full global pool")
		}

		foregroundAcquired := make(chan error, 1)
		go func() {
			release, err := limiter.AcquireForeground(ctx, 1)
			if err == nil {
				release()
			}
			foregroundAcquired <- err
		}()
		synctest.Wait()

		foreground[0]()
		synctest.Wait()
		select {
		case err := <-foregroundAcquired:
			if err != nil {
				t.Fatalf("foreground acquire: %v", err)
			}
		default:
			t.Fatal("foreground acquire was blocked by a refused background acquire")
		}
		for _, release := range foreground[1:] {
			release()
		}
	})
}

func TestBackgroundRefusedWhileForegroundQueued(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		limiter := NewGetFileLimiter()
		holdAll, err := limiter.AcquireForeground(ctx, MaxConcurrentGetFile)
		if err != nil {
			t.Fatalf("acquire all: %v", err)
		}
		queued := make(chan func(), 1)
		go func() {
			release, err := limiter.AcquireForeground(ctx, 1)
			if err != nil {
				t.Errorf("queued foreground: %v", err)
				return
			}
			queued <- release
		}()
		synctest.Wait()
		holdAll()
		synctest.Wait()
		release := <-queued
		defer release()
		if bg, ok := limiter.TryAcquireBackground(MaxConcurrentBackgroundGetFile); !ok {
			t.Fatal("background refused with free capacity")
		} else {
			bg()
		}
	})
}

func TestForegroundAcquireHonorsCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		limiter := NewGetFileLimiter()
		hold, err := limiter.AcquireForeground(context.Background(), MaxConcurrentGetFile)
		if err != nil {
			t.Fatalf("acquire all: %v", err)
		}
		defer hold()
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			release, err := limiter.AcquireForeground(ctx, 1)
			if err == nil {
				release()
			}
			result <- err
		}()
		synctest.Wait()
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("blocked foreground err = %v, want context canceled", err)
		}
	})
}

func TestGetFileReleaseIsIdempotentAndClamped(t *testing.T) {
	t.Parallel()

	limiter := NewGetFileLimiter()
	release, err := limiter.AcquireForeground(context.Background(), 1000)
	if err != nil {
		t.Fatalf("oversized acquire: %v", err)
	}
	release()
	release()
	zero, ok := limiter.TryAcquireBackground(0)
	if !ok {
		t.Fatal("zero weight background refused on an idle limiter")
	}
	zero()
	full, ok := limiter.TryAcquireBackground(1000)
	if !ok {
		t.Fatal("oversized background refused on an idle limiter")
	}
	if other, ok := limiter.TryAcquireBackground(1); ok {
		other()
		t.Fatal("background acquired beyond the clamped budget")
	}
	full()
}

func TestUploadPartSlotsBoundParts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		slots := NewUploadPartSlots()
		var releases []func()
		for i := 0; i < UploadThreads; i++ {
			release, err := slots.Acquire(context.Background())
			if err != nil {
				t.Fatalf("acquire %d: %v", i, err)
			}
			releases = append(releases, release)
		}
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			release, err := slots.Acquire(ctx)
			if err == nil {
				release()
			}
			result <- err
		}()
		synctest.Wait()
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("extra part err = %v, want context canceled", err)
		}
		for _, release := range releases {
			release()
		}
	})
}
