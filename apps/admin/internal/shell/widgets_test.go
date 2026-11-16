package shell

import (
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

func TestCopyFieldCopiesValue(t *testing.T) {
	test.NewTempApp(t)
	clip := &fakeClipboard{}
	field := NewCopyField("Key ID", "AKIA123", clip)
	test.NewTempWindow(t, field)
	button := findButton(t, field, "Copy")
	test.Tap(button)
	if clip.text != "AKIA123" {
		t.Fatalf("clipboard = %q, want AKIA123", clip.text)
	}
	if button.Text != "Copied" {
		t.Fatalf("button text = %q, want Copied", button.Text)
	}
	if !hasLabel(field, "AKIA123") || !hasLabel(field, "Key ID") {
		t.Fatalf("label or value missing")
	}
}

func TestProblemViewShowsTextAndCopiesDetails(t *testing.T) {
	test.NewTempApp(t)
	clip := &fakeClipboard{}
	p := model.Problem{
		Code: adminv1.CodeInternal, Title: "The server hit an error.", Body: "Body text", Hint: "Hint text",
		RequestID: "req-7", Details: map[string]any{"http_status": 500, "scope": "login"},
	}
	view := NewProblemView(p, clip)
	test.NewTempWindow(t, view)
	for _, want := range []string{"The server hit an error.", "Body text", "Hint text"} {
		if !hasLabel(view, want) {
			t.Errorf("view does not show %q", want)
		}
	}
	text := detailsText(p)
	for _, want := range []string{"code: internal", "http status: 500", "request id: req-7", "scope: login"} {
		if !strings.Contains(text, want) {
			t.Errorf("details %q missing %q", text, want)
		}
	}
	if strings.Index(text, "code:") > strings.Index(text, "http status:") {
		t.Errorf("details out of order: %q", text)
	}
	copyDetails := findButton(t, view, "Copy details")
	test.Tap(copyDetails)
	if clip.text != text {
		t.Fatalf("clipboard = %q, want %q", clip.text, text)
	}
}

func TestProblemViewWithoutDetailsHasNoExpander(t *testing.T) {
	test.NewTempApp(t)
	view := NewProblemView(model.Problem{Title: "Plain"}, &fakeClipboard{})
	test.NewTempWindow(t, view)
	if len(buttonsNamed(view, "Copy details")) != 0 {
		t.Fatalf("expander shown without details")
	}
}

func TestConfirmTypedEnablesConfirmOnExactWord(t *testing.T) {
	cases := []struct {
		name        string
		typed       string
		wantEnabled bool
	}{
		{name: "empty", typed: "", wantEnabled: false},
		{name: "partial", typed: "unli", wantEnabled: false},
		{name: "exact", typed: "unlink", wantEnabled: true},
		{name: "wrong case", typed: "Unlink", wantEnabled: false},
		{name: "trailing space", typed: "unlink ", wantEnabled: false},
		{name: "longer", typed: "unlinked", wantEnabled: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			test.NewTempApp(t)
			win := test.NewTempWindow(t, widget.NewLabel("x"))
			confirmed := 0
			c := newTypedConfirm(win, "Unlink Telegram", "The server stops.", "unlink", "Unlink", func() { confirmed++ })
			c.show()
			if !c.confirm.Disabled() {
				t.Fatalf("confirm enabled before typing")
			}
			test.Type(c.entry, tc.typed)
			if got := !c.confirm.Disabled(); got != tc.wantEnabled {
				t.Fatalf("confirm enabled = %v, want %v", got, tc.wantEnabled)
			}
			if tc.wantEnabled {
				test.Tap(c.confirm)
				if confirmed != 1 {
					t.Fatalf("onConfirm called %d times, want 1", confirmed)
				}
				if overlayShown(win) {
					t.Fatalf("dialog still open after confirm")
				}
				return
			}
			test.Tap(c.confirm)
			if confirmed != 0 {
				t.Fatalf("onConfirm called while disabled")
			}
		})
	}
}

func TestConfirmTypedEnterDoesNotConfirmAndEscapeCancels(t *testing.T) {
	test.NewTempApp(t)
	win := test.NewTempWindow(t, widget.NewLabel("x"))
	confirmed := 0
	c := newTypedConfirm(win, "Delete bucket", "Gone.", "photos", "Delete", func() { confirmed++ })
	c.show()
	if win.Canvas().Focused() != fyne.Focusable(c.entry) {
		t.Fatalf("entry is not focused on open")
	}
	test.Type(c.entry, "photos")
	c.entry.TypedKey(&fyne.KeyEvent{Name: fyne.KeyReturn})
	if confirmed != 0 {
		t.Fatalf("Enter in the entry confirmed a destructive action")
	}
	c.entry.TypedKey(&fyne.KeyEvent{Name: fyne.KeyEscape})
	if overlayShown(win) || confirmed != 0 {
		t.Fatalf("Escape: dialog shown %v, confirmed %d", overlayShown(win), confirmed)
	}
}

func TestConfirmDestructiveDefaultsToCancel(t *testing.T) {
	test.NewTempApp(t)
	win := test.NewTempWindow(t, widget.NewLabel("x"))
	confirmed := 0
	c := newDestructiveConfirm(win, "Revoke device", "Sure?", "Revoke", func() { confirmed++ })
	c.show()
	if win.Canvas().Focused() != fyne.Focusable(c.cancel) {
		t.Fatalf("cancel is not the focused default")
	}
	if c.confirm.Importance != widget.DangerImportance {
		t.Fatalf("confirm importance = %v, want danger", c.confirm.Importance)
	}
	c.cancel.TypedKey(&fyne.KeyEvent{Name: fyne.KeyReturn})
	if confirmed != 0 || overlayShown(win) {
		t.Fatalf("Enter on the default: confirmed %d, dialog shown %v", confirmed, overlayShown(win))
	}

	c = newDestructiveConfirm(win, "Revoke device", "Sure?", "Revoke", func() { confirmed++ })
	c.show()
	test.Tap(c.confirm)
	if confirmed != 1 || overlayShown(win) {
		t.Fatalf("confirm tap: confirmed %d, dialog shown %v", confirmed, overlayShown(win))
	}

	c = newDestructiveConfirm(win, "Revoke device", "Sure?", "Revoke", func() { confirmed++ })
	c.show()
	c.cancel.TypedKey(&fyne.KeyEvent{Name: fyne.KeyEscape})
	if confirmed != 1 || overlayShown(win) {
		t.Fatalf("Escape: confirmed %d, dialog shown %v", confirmed, overlayShown(win))
	}
}

func TestPublicConfirmEntryPointsOpenDialogs(t *testing.T) {
	test.NewTempApp(t)
	win := test.NewTempWindow(t, widget.NewLabel("x"))
	ConfirmTyped(win, "Unlink", "Body", "unlink", "Unlink", func() {})
	if !overlayShown(win) {
		t.Fatalf("ConfirmTyped opened no dialog")
	}
	win.Canvas().Overlays().Remove(win.Canvas().Overlays().Top())
	ConfirmDestructive(win, "Revoke", "Body", "Revoke", func() {})
	if !overlayShown(win) {
		t.Fatalf("ConfirmDestructive opened no dialog")
	}
}
