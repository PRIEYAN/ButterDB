package clock

import (
	"sync"
	"testing"
	"time"
)

func TestSystemNowIsCurrent(t *testing.T) {
	before := time.Now()
	got := System{}.Now()
	after := time.Now()
	if got.Before(before) || got.After(after) {
		t.Fatalf("System.Now = %v, want between %v and %v", got, before, after)
	}
}

func TestFakeClock(t *testing.T) {
	start := time.Unix(1763000000, 0).UTC()
	for _, tc := range []struct {
		name string
		act  func(f *Fake)
		want time.Time
	}{
		{"fresh fake returns start", func(*Fake) {}, start},
		{"advance moves forward", func(f *Fake) { f.Advance(90 * time.Second) }, start.Add(90 * time.Second)},
		{"advance accumulates", func(f *Fake) { f.Advance(time.Second); f.Advance(2 * time.Second) }, start.Add(3 * time.Second)},
		{"set replaces the time", func(f *Fake) { f.Set(start.Add(-time.Hour)) }, start.Add(-time.Hour)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := NewFake(start)
			tc.act(fake)
			if got := fake.Now(); !got.Equal(tc.want) {
				t.Fatalf("Now = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFakeClockIsSafeForConcurrentUse(t *testing.T) {
	fake := NewFake(time.Unix(0, 0))
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 100 {
				fake.Advance(time.Millisecond)
				_ = fake.Now()
			}
		})
	}
	wg.Wait()
	if got, want := fake.Now(), time.Unix(0, 0).Add(800*time.Millisecond); !got.Equal(want) {
		t.Fatalf("Now = %v, want %v", got, want)
	}
}

func TestClockInterfaceIsSatisfied(t *testing.T) {
	var _ Clock = System{}
	var _ Clock = NewFake(time.Time{})
}
