package shell

import (
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

func overlayShown(win fyne.Window) bool {
	return win.Canvas().Overlays().Top() != nil
}

func TestProblemPresentation(t *testing.T) {
	cases := []struct {
		name         string
		problem      model.Problem
		wantBanner   bool
		wantDialog   bool
		wantRoute    model.Route
		wantBannerTx string
	}{
		{name: "banner", problem: model.Problem{Title: "The server is rebuilding.", Presentation: model.PresentBanner}, wantBanner: true, wantBannerTx: "The server is rebuilding."},
		{name: "dialog", problem: model.Problem{Title: "The request is too large.", Presentation: model.PresentDialog}, wantDialog: true},
		{name: "inline falls back to dialog", problem: model.Problem{Title: "Check this value.", Presentation: model.PresentInline}, wantDialog: true},
		{name: "route shows dialog then navigates", problem: model.Problem{Title: "This device was revoked.", Presentation: model.PresentRoute, Route: model.RouteConnect}, wantDialog: true, wantRoute: model.RouteConnect},
		{name: "empty presentation shows nothing", problem: model.Problem{Presentation: ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, win := newTestShell(t)
			dashboard := newFakeScreen(model.RouteDashboard, fyne.Key1)
			connect := newFakeScreen(model.RouteConnect, "")
			s.Register(dashboard)
			s.Register(connect)
			s.Navigator().Navigate(model.RouteDashboard, nil)

			s.Notifier().Problem(tc.problem)

			if got := s.banner.visible(); got != tc.wantBanner {
				t.Errorf("banner visible = %v, want %v", got, tc.wantBanner)
			}
			if got := overlayShown(win); got != tc.wantDialog {
				t.Errorf("dialog shown = %v, want %v", got, tc.wantDialog)
			}
			wantCurrent := model.RouteDashboard
			if tc.wantRoute != "" {
				wantCurrent = tc.wantRoute
			}
			if s.Navigator().Current() != wantCurrent {
				t.Errorf("Current = %q, want %q", s.Navigator().Current(), wantCurrent)
			}
			if tc.wantBanner && !hasLabel(s.banner.root, tc.wantBannerTx) {
				t.Errorf("banner does not show %q", tc.wantBannerTx)
			}
		})
	}
}

func hasLabel(root fyne.CanvasObject, text string) bool {
	for _, o := range test.LaidOutObjects(root) {
		if l, ok := o.(*widget.Label); ok && l.Text == text {
			return true
		}
	}
	return false
}

func TestInfoAndClearBanner(t *testing.T) {
	s, _ := newTestShell(t)
	s.Notifier().Info("Settings saved.")
	if !s.banner.visible() || !hasLabel(s.banner.root, "Settings saved.") {
		t.Fatalf("info banner not shown")
	}
	s.Notifier().ClearBanner()
	if s.banner.visible() {
		t.Fatalf("banner still visible after ClearBanner")
	}
}

func TestBannerClearsOnNavigation(t *testing.T) {
	s, _ := newTestShell(t)
	s.Register(newFakeScreen(model.RouteDashboard, fyne.Key1))
	s.Notifier().Problem(model.Problem{Title: "Offline", Presentation: model.PresentBanner})
	s.Navigator().Navigate(model.RouteDashboard, nil)
	if s.banner.visible() {
		t.Fatalf("banner survived navigation")
	}
}

func TestBannerRetryReshowsCurrentScreen(t *testing.T) {
	s, _ := newTestShell(t)
	dashboard := newFakeScreen(model.RouteDashboard, fyne.Key1)
	s.Register(dashboard)
	s.Navigator().Navigate(model.RouteDashboard, nil)
	s.Notifier().Problem(model.Problem{Title: "Cannot reach the server.", Presentation: model.PresentBanner, Retryable: true})

	retry := findButton(t, s.banner.root, "Retry")
	test.Tap(retry)
	if len(dashboard.shown) != 2 {
		t.Fatalf("dashboard shown %d times after Retry, want 2", len(dashboard.shown))
	}
	if s.banner.visible() {
		t.Fatalf("banner still visible after Retry")
	}
}

func TestBannerRetryWaitsForCountdown(t *testing.T) {
	s, _ := newTestShell(t)
	s.Register(newFakeScreen(model.RouteDashboard, fyne.Key1))
	s.Navigator().Navigate(model.RouteDashboard, nil)
	s.Notifier().Problem(model.Problem{
		Title: "Telegram asked the server to wait.", Presentation: model.PresentBanner, Retryable: true,
		RetryAt: s.now().Add(30 * time.Second),
	})
	retry := findButton(t, s.banner.root, "Retry")
	steps := []struct {
		progress    float32
		wantText    string
		wantEnabled bool
	}{
		{progress: 0, wantText: "Try again in 30 s.", wantEnabled: false},
		{progress: 0.5, wantText: "Try again in 15 s.", wantEnabled: false},
		{progress: 1, wantText: "You can retry now.", wantEnabled: true},
	}
	for _, step := range steps {
		s.banner.tick(step.progress)
		if !hasLabel(s.banner.root, step.wantText) {
			t.Fatalf("progress %v: countdown text %q missing", step.progress, step.wantText)
		}
		if got := !retry.Disabled(); got != step.wantEnabled {
			t.Fatalf("progress %v: Retry enabled = %v, want %v", step.progress, got, step.wantEnabled)
		}
	}
	s.Notifier().ClearBanner()
	if s.banner.tick != nil {
		t.Fatalf("countdown still active after ClearBanner")
	}
}

func TestBannerShowsDetailsWithoutRetryWhenNotRetryable(t *testing.T) {
	s, _ := newTestShell(t)
	s.Notifier().Problem(model.Problem{Code: adminv1.CodeBucketNotFound, Title: "That bucket no longer exists.", Presentation: model.PresentBanner, RequestID: "req-9"})
	if len(buttonsNamed(s.banner.root, "Retry")) != 0 {
		t.Fatalf("non-retryable banner has a Retry button")
	}
	if len(buttonsNamed(s.banner.root, "Dismiss")) != 1 {
		t.Fatalf("banner has no Dismiss button")
	}
}

func buttonsNamed(root fyne.CanvasObject, text string) []*widget.Button {
	var out []*widget.Button
	for _, o := range test.LaidOutObjects(root) {
		if b, ok := o.(*widget.Button); ok && b.Text == text {
			out = append(out, b)
		}
	}
	return out
}

func findButton(t *testing.T, root fyne.CanvasObject, text string) *widget.Button {
	t.Helper()
	found := buttonsNamed(root, text)
	if len(found) == 0 {
		t.Fatalf("no button %q", text)
	}
	return found[0]
}

func TestProblemDialogClosesOnEscapeAndFocusesClose(t *testing.T) {
	s, win := newTestShell(t)
	s.Notifier().Problem(model.Problem{Title: "The request is too large.", Presentation: model.PresentDialog})
	if !overlayShown(win) {
		t.Fatalf("no dialog shown")
	}
	focused, ok := win.Canvas().Focused().(*keyButton)
	if !ok || focused.Text != "Close" {
		t.Fatalf("focused = %T, want the Close button", win.Canvas().Focused())
	}
	focused.TypedKey(&fyne.KeyEvent{Name: fyne.KeyEscape})
	if overlayShown(win) {
		t.Fatalf("dialog still open after Escape")
	}
}
