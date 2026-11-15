package buckets

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"
)

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestWriteGatesExclusiveWaitsForShared(t *testing.T) {
	gates := NewWriteGates()
	ctx := context.Background()
	releaseShared, err := gates.AcquireShared(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	other, err := gates.AcquireExclusive(ctx, 8)
	if err != nil {
		t.Fatalf("a different channel must not block: %v", err)
	}
	other()

	waitCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := gates.AcquireExclusive(waitCtx, 7); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("exclusive acquire error = %v, want deadline exceeded", err)
	}
	releaseShared()
	releaseShared()
	releaseExclusive, err := gates.AcquireExclusive(ctx, 7)
	if err != nil {
		t.Fatalf("exclusive after release: %v", err)
	}
	shortCtx, shortCancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer shortCancel()
	if _, err := gates.AcquireShared(shortCtx, 7); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shared acquire during exclusive error = %v", err)
	}
	releaseExclusive()
}

func TestJobIDs(t *testing.T) {
	t.Run("minted ids are canonical uuid v4", func(t *testing.T) {
		random := &counterReader{}
		first, err := newJobID(random)
		if err != nil {
			t.Fatal(err)
		}
		second, _ := newJobID(random)
		if !uuidV4.MatchString(first) || first == second {
			t.Fatalf("ids = %q, %q", first, second)
		}
	})
	t.Run("short reader fails", func(t *testing.T) {
		if _, err := newJobID(strings.NewReader("short")); err == nil {
			t.Fatal("newJobID accepted a short reader")
		}
	})
	t.Run("derived create id is stable per channel", func(t *testing.T) {
		a := derivedCreateJobID(testDeploymentID, 11)
		if a != derivedCreateJobID(testDeploymentID, 11) || a == derivedCreateJobID(testDeploymentID, 12) || !uuidV4.MatchString(a) {
			t.Fatalf("derived id = %q", a)
		}
	})
}
