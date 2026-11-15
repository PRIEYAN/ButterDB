package phase06

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPutAndDeleteShareOneKeyLock(t *testing.T) {
	w := newWorld(t)
	info := w.createBucket("photos", "none")
	w.mustPut("photos", "doc.txt", []byte("first"))

	release, err := w.write.KeyLocks.Lock(context.Background(), info.ChannelID, "doc.txt")
	if err != nil {
		t.Fatalf("take the shared key lock: %v", err)
	}
	waiting, cancel := context.WithTimeout(w.callerContext(), 50*time.Millisecond)
	defer cancel()
	if err := w.write.Remove.Delete(waiting, "photos", "doc.txt"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Delete while the key is held = %v, want a deadline error", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := w.put("photos", "doc.txt", []byte("second"))
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("Put finished while the key was held: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Put after release: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Put never finished after the key lock was released")
	}
}
