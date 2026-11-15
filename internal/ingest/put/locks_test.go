package put

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestKeyedLockerSerializesSameKeyAndAllowsDifferentKeys(t *testing.T) {
	t.Parallel()

	locker := NewKeyedLocker()
	releaseA, err := locker.Lock(context.Background(), "bucket/object-a")
	if err != nil {
		t.Fatalf("lock a: %v", err)
	}

	differentDone := make(chan struct{})
	go func() {
		release, lockErr := locker.Lock(context.Background(), "bucket/object-b")
		if lockErr == nil {
			release()
		}
		close(differentDone)
	}()
	select {
	case <-differentDone:
	case <-time.After(time.Second):
		t.Fatal("different key was unnecessarily blocked")
	}

	sameAcquired := make(chan struct{})
	go func() {
		release, lockErr := locker.Lock(context.Background(), "bucket/object-a")
		if lockErr == nil {
			close(sameAcquired)
			release()
		}
	}()
	select {
	case <-sameAcquired:
		t.Fatal("same key acquired before release")
	case <-time.After(30 * time.Millisecond):
	}
	releaseA()
	select {
	case <-sameAcquired:
	case <-time.After(time.Second):
		t.Fatal("same key did not acquire after release")
	}
}

func TestKeyedLockerSortsDeduplicatesAndCleansEntries(t *testing.T) {
	t.Parallel()

	locker := NewKeyedLocker()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			release, err := locker.Lock(context.Background(), "b", "a", "a")
			if err == nil {
				release()
			}
		}()
		go func() {
			defer wg.Done()
			release, err := locker.Lock(context.Background(), "a", "b")
			if err == nil {
				release()
			}
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("sorted multi-key locks deadlocked")
	}
	if got := locker.Len(); got != 0 {
		t.Fatalf("retained lock entries = %d, want 0", got)
	}
}

func TestKeyedLockerHonorsCanceledWait(t *testing.T) {
	t.Parallel()

	locker := NewKeyedLocker()
	release, err := locker.Lock(context.Background(), "busy")
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := locker.Lock(ctx, "busy"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("canceled lock error = %v, want deadline exceeded", err)
	}
	if got := locker.Len(); got != 1 {
		t.Fatalf("lock entries after canceled waiter = %d, want 1 held entry", got)
	}
}

func TestSharedKeyLocksSerializeWritersOfOneKeyAcrossPutters(t *testing.T) {
	w := newWorld(t, "none")
	second, err := New(w.deps)
	if err != nil {
		t.Fatalf("second putter: %v", err)
	}
	external, err := w.deps.KeyLocks.Lock(w.ctx, testChannelID, "shared.txt")
	if err != nil {
		t.Fatalf("external lock: %v", err)
	}
	released := false
	defer func() {
		if !released {
			external()
		}
	}()

	services := []Service{w.svc, second}
	done := make(chan error, len(services))
	for index, service := range services {
		body := []byte{byte('a' + index), 'b', 'c'}
		go func() {
			_, putErr := service.Put(w.ctx, w.putRequest("shared.txt", len(body)), bytes.NewReader(body))
			done <- putErr
		}()
	}
	select {
	case err := <-done:
		t.Fatalf("put finished while another writer held the key lock: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	if got := len(w.sentControls()); got != 0 {
		t.Fatalf("controls sent while key lock was held = %d, want 0", got)
	}

	other, err := w.deps.KeyLocks.Lock(w.ctx, testChannelID, "other.txt")
	if err != nil {
		t.Fatalf("other key lock: %v", err)
	}
	other()

	released = true
	external()
	for range services {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("put after release: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("put did not finish after the key lock was released")
		}
	}
	if got := len(w.sentControls()); got != 2 {
		t.Fatalf("controls sent = %d, want 2", got)
	}
}
