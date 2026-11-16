package shell

import (
	"testing"
	"time"
)

func TestFormatBytes(t *testing.T) {
	cases := []struct {
		name string
		n    int64
		want string
	}{
		{name: "negative clamps", n: -1, want: "0 B"},
		{name: "zero", n: 0, want: "0 B"},
		{name: "one byte", n: 1, want: "1 B"},
		{name: "just below a KiB", n: 1023, want: "1023 B"},
		{name: "one KiB", n: 1024, want: "1.0 KiB"},
		{name: "one and a half KiB", n: 1536, want: "1.5 KiB"},
		{name: "just below a MiB", n: 1024*1024 - 1, want: "1024.0 KiB"},
		{name: "one MiB", n: 1024 * 1024, want: "1.0 MiB"},
		{name: "twenty MiB", n: 20 * 1024 * 1024, want: "20.0 MiB"},
		{name: "object ceiling", n: 1900 * 1024 * 1024, want: "1.9 GiB"},
		{name: "one TiB", n: 1 << 40, want: "1.0 TiB"},
		{name: "one PiB", n: 1 << 50, want: "1.0 PiB"},
		{name: "max int64", n: 1<<63 - 1, want: "8.0 EiB"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FormatBytes(tc.n); got != tc.want {
				t.Fatalf("FormatBytes(%d) = %q, want %q", tc.n, got, tc.want)
			}
		})
	}
}

func TestFormatAge(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		t    time.Time
		want string
	}{
		{name: "zero time", t: time.Time{}, want: "never"},
		{name: "future", t: now.Add(time.Hour), want: "just now"},
		{name: "same instant", t: now, want: "just now"},
		{name: "59 seconds", t: now.Add(-59 * time.Second), want: "just now"},
		{name: "one minute", t: now.Add(-time.Minute), want: "1 min ago"},
		{name: "59 minutes", t: now.Add(-59 * time.Minute), want: "59 min ago"},
		{name: "one hour", t: now.Add(-time.Hour), want: "1 h ago"},
		{name: "23 hours", t: now.Add(-23*time.Hour - 59*time.Minute), want: "23 h ago"},
		{name: "one day", t: now.Add(-24 * time.Hour), want: "1 d ago"},
		{name: "29 days", t: now.Add(-29 * 24 * time.Hour), want: "29 d ago"},
		{name: "30 days shows the date", t: now.Add(-30 * 24 * time.Hour), want: "2026-10-17"},
		{name: "last year", t: time.Date(2025, 3, 2, 8, 0, 0, 0, time.UTC), want: "2025-03-02"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FormatAge(tc.t, now); got != tc.want {
				t.Fatalf("FormatAge = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCountdownText(t *testing.T) {
	cases := []struct {
		name string
		d    time.Duration
		want string
	}{
		{name: "negative", d: -time.Second, want: "You can retry now."},
		{name: "zero", d: 0, want: "You can retry now."},
		{name: "fraction rounds up", d: 200 * time.Millisecond, want: "Try again in 1 s."},
		{name: "thirty seconds", d: 30 * time.Second, want: "Try again in 30 s."},
		{name: "one minute", d: time.Minute, want: "Try again in 1 min 0 s."},
		{name: "ninety seconds", d: 90 * time.Second, want: "Try again in 1 min 30 s."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := countdownText(tc.d); got != tc.want {
				t.Fatalf("countdownText(%v) = %q, want %q", tc.d, got, tc.want)
			}
		})
	}
}
