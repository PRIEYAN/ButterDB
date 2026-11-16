package model

import (
	"sync"
	"testing"
)

func TestObservableNotifiesSubscribers(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		initial int
		sets    []int
		want    []int
	}{
		{name: "one set", initial: 0, sets: []int{1}, want: []int{1}},
		{name: "ordered sets", initial: 5, sets: []int{6, 7, 8}, want: []int{6, 7, 8}},
		{name: "same value still notifies", initial: 1, sets: []int{1, 1}, want: []int{1, 1}},
		{name: "no sets", initial: 3, sets: nil, want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			o := NewObservable(tc.initial)
			var first, second []int
			o.Subscribe(func(v int) { first = append(first, v) })
			o.Subscribe(func(v int) { second = append(second, v) })
			for _, v := range tc.sets {
				o.Set(v)
			}
			if len(first) != len(tc.want) || len(second) != len(tc.want) {
				t.Fatalf("notifications = %v and %v, want %v", first, second, tc.want)
			}
			for i, v := range tc.want {
				if first[i] != v || second[i] != v {
					t.Fatalf("notification %d = %d and %d, want %d", i, first[i], second[i], v)
				}
			}
			wantFinal := tc.initial
			if len(tc.sets) > 0 {
				wantFinal = tc.sets[len(tc.sets)-1]
			}
			if got := o.Get(); got != wantFinal {
				t.Fatalf("Get = %d, want %d", got, wantFinal)
			}
		})
	}
}

func TestObservableSubscriberMayUseObservable(t *testing.T) {
	t.Parallel()
	o := NewObservable("a")
	var seen string
	o.Subscribe(func(v string) { seen = v + o.Get() })
	o.Set("b")
	if seen != "bb" {
		t.Fatalf("seen = %q, want %q", seen, "bb")
	}
}

func TestObservableCancel(t *testing.T) {
	t.Parallel()
	o := NewObservable(0)
	var kept, dropped int
	o.Subscribe(func(int) { kept++ })
	cancel := o.Subscribe(func(int) { dropped++ })
	o.Set(1)
	cancel()
	cancel()
	o.Set(2)
	if kept != 2 || dropped != 1 {
		t.Fatalf("kept = %d, dropped = %d, want 2 and 1", kept, dropped)
	}
}

func TestObservableConcurrentUse(t *testing.T) {
	t.Parallel()
	o := NewObservable(0)
	var mu sync.Mutex
	total := 0
	o.Subscribe(func(v int) {
		mu.Lock()
		total += v
		mu.Unlock()
	})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				o.Set(1)
				_ = o.Get()
			}
		}()
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if total != 800 {
		t.Fatalf("total = %d, want 800", total)
	}
}

func TestObservableZeroValueUsable(t *testing.T) {
	t.Parallel()
	var o Observable[int]
	called := 0
	o.Subscribe(func(int) { called++ })
	o.Set(4)
	if called != 1 || o.Get() != 4 {
		t.Fatalf("called = %d, Get = %d", called, o.Get())
	}
}
