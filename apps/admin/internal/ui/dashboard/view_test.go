package dashboard

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/dashboard/vm"
)

var testNow = time.Date(2026, 11, 16, 9, 30, 0, 0, time.UTC)

type fakeClient struct {
	mu        sync.Mutex
	status    adminv1.Status
	usage     adminv1.Usage
	statusErr error
	calls     int
}

func (f *fakeClient) Status(context.Context) (adminv1.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.status, f.statusErr
}

func (f *fakeClient) Usage(context.Context) (adminv1.Usage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.usage, nil
}

type fakeClipboard struct{ text string }

func (c *fakeClipboard) SetText(text string) { c.text = text }
func (c *fakeClipboard) Text() string        { return c.text }

type fakeNav struct{ navigated []model.Route }

func (n *fakeNav) Navigate(r model.Route, _ model.Params) { n.navigated = append(n.navigated, r) }
func (n *fakeNav) Current() model.Route                   { return model.RouteDashboard }
func (n *fakeNav) SetEnabled(model.Route, bool, string)   {}

type fakeNotifier struct {
	problems []model.Problem
	cleared  int
}

func (n *fakeNotifier) Problem(p model.Problem) { n.problems = append(n.problems, p) }
func (n *fakeNotifier) Info(string)             {}
func (n *fakeNotifier) ClearBanner()            { n.cleared++ }

func sampleClient() *fakeClient {
	until := testNow.Add(time.Minute)
	return &fakeClient{
		status: adminv1.Status{
			ServerVersion: "0.1.0",
			Commit:        "b73a0ec",
			State:         "ready",
			UptimeSeconds: 90061,
			Telegram: adminv1.TelegramHealth{
				Linked:            true,
				Connected:         true,
				Account:           &adminv1.TelegramAccount{FirstName: "Ada", Username: "ada"},
				FloodWaitUntil:    &until,
				SendRatePerMinute: 20,
			},
			Components: []adminv1.Component{
				{Name: "telegram", Status: "ok", Since: testNow.Add(-2 * time.Hour)},
				{Name: "sync", Status: "degraded", Detail: "catching up", Since: testNow.Add(-5 * time.Minute)},
			},
			Endpoints: adminv1.Endpoints{S3PublicURL: "https://s3.example.com", S3Region: "us-east-1"},
			Replica:   adminv1.ReplicaSummary{State: "not_available"},
		},
		usage: adminv1.Usage{
			Buckets:          3,
			Objects:          18234,
			StoredBytes:      2 * 1024 * 1024,
			EncryptedObjects: 12,
			BlockCache:       adminv1.CacheUsage{UsedBytes: 1 << 30, LimitBytes: 2 << 30, HitRatio1h: 0.93},
			Spool:            adminv1.SpoolUsage{JobsInFlight: 2, LimitBytes: 1 << 30},
		},
	}
}

func newTestScreen(t *testing.T, c *fakeClient, nav *fakeNav, n *fakeNotifier, clip *fakeClipboard, opts ...vm.Option) (*Screen, *vm.ViewModel) {
	t.Helper()
	test.NewTempApp(t)
	v := vm.New(c, func() time.Time { return testNow }, opts...)
	var s *Screen
	if n != nil {
		s = NewScreen(v, nav, n, clip)
	} else {
		s = NewScreen(v, nav, nil, clip)
	}
	test.NewTempWindow(t, s.Content())
	return s, v
}

func walk(o fyne.CanvasObject, fn func(fyne.CanvasObject)) {
	if o == nil {
		return
	}
	fn(o)
	switch c := o.(type) {
	case *fyne.Container:
		for _, child := range c.Objects {
			walk(child, fn)
		}
	case *widget.Label, *widget.Button:
	case fyne.Widget:
		for _, child := range test.WidgetRenderer(c).Objects() {
			walk(child, fn)
		}
	}
}

func visibleTexts(o fyne.CanvasObject) []string {
	var out []string
	walk(o, func(co fyne.CanvasObject) {
		if !co.Visible() {
			return
		}
		switch w := co.(type) {
		case *widget.Label:
			out = append(out, w.Text)
		case *widget.Button:
			out = append(out, w.Text)
		case *shell.WindowCard:
			out = append(out, w.Title)
		case *shell.Badge:
			out = append(out, w.Text())
		}
	})
	return out
}

func hidden(o fyne.CanvasObject, title string) bool {
	found := false
	walk(o, func(co fyne.CanvasObject) {
		if card, ok := co.(*shell.WindowCard); ok && card.Title == title {
			found = !card.Visible()
		}
	})
	return found
}

func joined(o fyne.CanvasObject) string {
	return strings.Join(visibleTexts(o), "\n")
}

func TestScreenIdentity(t *testing.T) {
	s, _ := newTestScreen(t, sampleClient(), &fakeNav{}, nil, &fakeClipboard{})
	if s.Route() != model.RouteDashboard || s.Shortcut() != fyne.Key1 || s.Title() != "Dashboard" || s.Icon() == nil {
		t.Fatalf("screen identity = %q %q %q", s.Route(), s.Shortcut(), s.Title())
	}
}

func TestRendersMergedStatusAndUsage(t *testing.T) {
	s, v := newTestScreen(t, sampleClient(), &fakeNav{}, nil, &fakeClipboard{})
	v.Refresh(context.Background())
	text := joined(s.Content())
	for _, want := range []string{
		"State: ready",
		"0.1.0 (b73a0ec)",
		"1 d 1 h",
		"Ada @ada",
		"Connected",
		"Flood wait until 09:31:00",
		"20 per minute",
		"18234",
		"2.0 MiB",
		"1.0 GiB of 2.0 GiB",
		"93%",
		"telegram: ok (2 h ago)",
		"sync: degraded — catching up (5 min ago)",
		"Use these with any AWS SDK: endpoint, region, path-style addressing.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("rendered text lacks %q\n%s", want, text)
		}
	}
	if !hidden(s.Content(), "Replica") {
		t.Errorf("replica card should be hidden for not_available")
	}
}

func TestBlockCacheOffShowsOff(t *testing.T) {
	c := sampleClient()
	c.usage.BlockCache = adminv1.CacheUsage{HitRatio1h: 0.5}
	s, v := newTestScreen(t, c, &fakeNav{}, nil, &fakeClipboard{})
	v.Refresh(context.Background())
	text := joined(s.Content())
	for _, want := range []string{"Block cache", "Off", "Spool"} {
		if !strings.Contains(text, want) {
			t.Errorf("rendered text lacks %q\n%s", want, text)
		}
	}
	for _, unwanted := range []string{"0 B of 0 B", "no limit set", "Hit ratio", "50%"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("block cache off still renders %q\n%s", unwanted, text)
		}
	}
}

func TestReplicaCardShownWhenAvailable(t *testing.T) {
	c := sampleClient()
	c.status.Replica = adminv1.ReplicaSummary{State: "linked", PendingOps: 4}
	s, v := newTestScreen(t, c, &fakeNav{}, nil, &fakeClipboard{})
	v.Refresh(context.Background())
	if hidden(s.Content(), "Replica") {
		t.Fatalf("replica card hidden")
	}
	text := joined(s.Content())
	if !strings.Contains(text, "linked") || !strings.Contains(text, "Pending ops") {
		t.Fatalf("replica rows missing\n%s", text)
	}
}

func TestEndpointCopyUsesClipboard(t *testing.T) {
	clip := &fakeClipboard{}
	s, v := newTestScreen(t, sampleClient(), &fakeNav{}, nil, clip)
	v.Refresh(context.Background())
	var buttons []*widget.Button
	walk(s.Content(), func(co fyne.CanvasObject) {
		if b, ok := co.(*widget.Button); ok && b.Text == "Copy" {
			buttons = append(buttons, b)
		}
	})
	if len(buttons) != 2 {
		t.Fatalf("copy buttons = %d, want 2", len(buttons))
	}
	test.Tap(buttons[0])
	if clip.text != "https://s3.example.com" {
		t.Fatalf("clipboard = %q", clip.text)
	}
	test.Tap(buttons[1])
	if clip.text != "us-east-1" {
		t.Fatalf("clipboard = %q", clip.text)
	}
}

func TestUnconfiguredEndpointExplained(t *testing.T) {
	c := sampleClient()
	c.status.Endpoints.S3PublicURL = ""
	s, v := newTestScreen(t, c, &fakeNav{}, nil, &fakeClipboard{})
	v.Refresh(context.Background())
	if !strings.Contains(joined(s.Content()), "not configured") {
		t.Fatalf("missing unconfigured text\n%s", joined(s.Content()))
	}
}

func TestRefreshButtonRefetches(t *testing.T) {
	c := sampleClient()
	s, v := newTestScreen(t, c, &fakeNav{}, nil, &fakeClipboard{})
	settled := make(chan struct{}, 4)
	cancel := v.State().Subscribe(func(st vm.State) {
		if !st.Loading && st.HasStatus {
			settled <- struct{}{}
		}
	})
	defer cancel()
	var refresh *widget.Button
	walk(s.Content(), func(co fyne.CanvasObject) {
		if b, ok := co.(*widget.Button); ok && b.Text == "Refresh" {
			refresh = b
		}
	})
	if refresh == nil {
		t.Fatalf("no refresh button")
	}
	test.Tap(refresh)
	select {
	case <-settled:
	case <-time.After(5 * time.Second):
		t.Fatalf("refresh button did not fetch")
	}
	c.mu.Lock()
	calls := c.calls
	c.mu.Unlock()
	if calls != 1 {
		t.Fatalf("status calls = %d", calls)
	}
}

func TestProblemRoutesThroughNotifier(t *testing.T) {
	tests := []struct {
		name         string
		err          error
		wantNotified int
		wantRoutes   int
	}{
		{"route problem goes to notifier", &adminv1.APIError{Status: 401, Body: adminv1.ErrorBody{Code: adminv1.CodeUnauthenticated}}, 1, 0},
		{"internal problem shows a dialog once", &adminv1.APIError{Status: 500, Body: adminv1.ErrorBody{Code: adminv1.CodeInternal, RequestID: "r1"}}, 1, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := sampleClient()
			c.statusErr = tt.err
			nav, n := &fakeNav{}, &fakeNotifier{}
			s, v := newTestScreen(t, c, nav, n, &fakeClipboard{})
			v.Refresh(context.Background())
			v.Refresh(context.Background())
			if len(n.problems) != tt.wantNotified || len(nav.navigated) != tt.wantRoutes {
				t.Fatalf("notified %d navigated %d", len(n.problems), len(nav.navigated))
			}
			if !strings.Contains(joined(s.Content()), n.problems[0].Title) {
				t.Fatalf("inline strip lacks problem title")
			}
		})
	}
}

func TestBannerClearedAfterRecovery(t *testing.T) {
	c := sampleClient()
	c.statusErr = &adminv1.APIError{Status: 503, Body: adminv1.ErrorBody{Code: adminv1.CodeServerNotReady, Details: map[string]any{"state": "syncing"}}}
	n := &fakeNotifier{}
	_, v := newTestScreen(t, c, &fakeNav{}, n, &fakeClipboard{})
	v.Refresh(context.Background())
	if len(n.problems) != 1 || n.problems[0].Presentation != model.PresentBanner {
		t.Fatalf("problems = %+v", n.problems)
	}
	c.mu.Lock()
	c.statusErr = nil
	c.mu.Unlock()
	v.Refresh(context.Background())
	if n.cleared != 1 {
		t.Fatalf("ClearBanner calls = %d", n.cleared)
	}
}

func TestRouteProblemNavigatesWithoutNotifier(t *testing.T) {
	c := sampleClient()
	c.statusErr = &adminv1.APIError{Status: 401, Body: adminv1.ErrorBody{Code: adminv1.CodeUnauthenticated}}
	nav := &fakeNav{}
	test.NewTempApp(t)
	v := vm.New(c, func() time.Time { return testNow })
	content := NewView(v, nav)
	test.NewTempWindow(t, content)
	v.Refresh(context.Background())
	if len(nav.navigated) != 1 || nav.navigated[0] != model.RouteTelegram {
		t.Fatalf("navigated = %v", nav.navigated)
	}
}

func TestShowStartsPollingAndHideStops(t *testing.T) {
	c := sampleClient()
	ticks := make(chan time.Time)
	requests := make(chan time.Duration, 8)
	wait := func(d time.Duration) (<-chan time.Time, func()) {
		requests <- d
		return ticks, func() {}
	}
	s, _ := newTestScreen(t, c, &fakeNav{}, nil, &fakeClipboard{}, vm.WithWaiter(wait))
	s.Show(context.Background(), nil)
	select {
	case <-requests:
	case <-time.After(5 * time.Second):
		t.Fatalf("poll did not start on Show")
	}
	s.Hide()
	c.mu.Lock()
	before := c.calls
	c.mu.Unlock()
	select {
	case ticks <- testNow:
		t.Fatalf("poll still running after Hide")
	default:
	}
	c.mu.Lock()
	after := c.calls
	c.mu.Unlock()
	if before != after || before != 1 {
		t.Fatalf("calls before %d after %d", before, after)
	}
}
