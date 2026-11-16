package shell

import (
	"image/color"
	"testing"

	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

func TestBadgeText(t *testing.T) {
	cases := []struct {
		name  string
		parts []BadgePart
		want  string
	}{
		{name: "single value", parts: []BadgePart{{Value: "Off"}}, want: "Off"},
		{name: "key and value", parts: []BadgePart{{Key: "policy", Value: "private"}}, want: "POLICY: private"},
		{name: "pairs split by a hairline", parts: []BadgePart{{Key: "policy", Value: "private"}, {Key: "enc", Value: "TDE1"}}, want: "POLICY: private | ENC: TDE1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			test.NewTempApp(t)
			badge := NewBadge(tc.parts...)
			test.NewTempWindow(t, badge)
			if got := badge.Text(); got != tc.want {
				t.Fatalf("Text() = %q, want %q", got, tc.want)
			}
			if badge.MinSize().Width <= 0 || badge.MinSize().Height <= 0 {
				t.Fatalf("badge has no size: %v", badge.MinSize())
			}
		})
	}
}

func TestActionFrameFollowsControlVisibility(t *testing.T) {
	test.NewTempApp(t)
	button := widget.NewButton("Retry", nil)
	framed := Action(button)
	test.NewTempWindow(t, framed)
	if !framed.Visible() {
		t.Fatalf("frame hidden while its button is visible")
	}
	button.Hide()
	if framed.Visible() {
		t.Fatalf("frame still visible after its button was hidden")
	}
	if framed.MinSize() != button.MinSize() {
		t.Fatalf("frame min size %v differs from the button's %v", framed.MinSize(), button.MinSize())
	}
}

func TestActionHoverInvertsToPink(t *testing.T) {
	app := test.NewTempApp(t)
	app.Settings().SetTheme(NewParchmentTheme())
	button := widget.NewButton("Create bucket", nil)
	button.Importance = widget.HighImportance
	frame := Action(button).(*actionFrame)
	test.NewTempWindow(t, frame)
	pink := rgb(accentPink)
	ink := rgb(inkPrimary)
	cases := []struct {
		name        string
		hovered     bool
		wantFill    color.NRGBA
		wantOnLabel color.NRGBA
	}{
		{name: "at rest the primary button is ink with parchment text", hovered: false, wantFill: ink, wantOnLabel: rgb(surfaceParchment)},
		{name: "hover inverts to pink with ink text", hovered: true, wantFill: pink, wantOnLabel: ink},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.hovered {
				frame.hover.MouseIn(nil)
			} else {
				frame.hover.MouseOut()
			}
			fill := color.NRGBAModel.Convert(frame.inverted.Color(theme.ColorNamePrimary, theme.VariantLight))
			label := color.NRGBAModel.Convert(frame.inverted.Color(theme.ColorNameForegroundOnPrimary, theme.VariantLight))
			if fill != tc.wantFill || label != tc.wantOnLabel {
				t.Fatalf("fill %v label %v, want %v and %v", fill, label, tc.wantFill, tc.wantOnLabel)
			}
		})
	}
}

func TestCalloutHiddenWithContent(t *testing.T) {
	test.NewTempApp(t)
	label := widget.NewLabel("Anyone with the URL can read every object in this bucket.")
	callout := NewCallout(CalloutWarning, label)
	test.NewTempWindow(t, callout)
	if !callout.Visible() {
		t.Fatalf("callout hidden while its content is visible")
	}
	label.Hide()
	if callout.Visible() {
		t.Fatalf("callout still visible after its content was hidden")
	}
}

func TestWindowCardLabelAndBand(t *testing.T) {
	app := test.NewTempApp(t)
	app.Settings().SetTheme(NewTerminalTheme())
	card := NewHighlightCard("dashboard/server", "Server", widget.NewLabel("State: ready"))
	test.NewTempWindow(t, card)
	card.SetLabel("dashboard/status")
	if card.Label != "dashboard/status" || card.Title != "Server" {
		t.Fatalf("label %q title %q", card.Label, card.Title)
	}
	text := color.NRGBAModel.Convert(onAccentTheme{}.Color(theme.ColorNameForeground, theme.VariantDark))
	if text != rgb(inkPrimary) {
		t.Fatalf("text on the pink band = %v, want ink", text)
	}
	if !hasLabel(card, "State: ready") || !hasLabel(card, "Server") {
		t.Fatalf("card does not render its title and content")
	}
}

func TestDialogFramesButtonsAndReportsClose(t *testing.T) {
	test.NewTempApp(t)
	win := test.NewTempWindow(t, widget.NewLabel("x"))
	closed := 0
	dlg := NewDialog(win, "buckets/new", "New bucket", widget.NewLabel("body"))
	dlg.SetOnClosed(func() { closed++ })
	dlg.SetButtons(widget.NewButton("Cancel", func() { dlg.Hide() }))
	dlg.Show()
	if !overlayShown(win) {
		t.Fatalf("dialog not shown")
	}
	if dlg.Card().Label != "buckets/new" || dlg.Card().Title != "New bucket" {
		t.Fatalf("card label %q title %q", dlg.Card().Label, dlg.Card().Title)
	}
	test.Tap(findButton(t, win.Canvas().Overlays().Top(), "Cancel"))
	if overlayShown(win) || closed != 1 {
		t.Fatalf("after Cancel: shown %v, closed %d", overlayShown(win), closed)
	}
}

func TestExpanderRevealsContent(t *testing.T) {
	test.NewTempApp(t)
	body := widget.NewLabel("code: internal")
	e := newExpander("Details", body)
	test.NewTempWindow(t, e)
	if body.Visible() || e.open {
		t.Fatalf("expander starts open")
	}
	test.Tap(e.header)
	if !body.Visible() || !e.open {
		t.Fatalf("expander did not open")
	}
	test.Tap(e.header)
	if body.Visible() || e.open {
		t.Fatalf("expander did not close")
	}
}

func TestMeterClampsFraction(t *testing.T) {
	cases := []struct {
		name     string
		fraction float64
		want     float64
	}{
		{name: "below zero", fraction: -0.5, want: 0},
		{name: "inside range", fraction: 0.25, want: 0.25},
		{name: "above one", fraction: 3, want: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			test.NewTempApp(t)
			bar := NewMeterBar(tc.fraction).(*meterBar)
			if bar.fraction != tc.want {
				t.Fatalf("fraction = %v, want %v", bar.fraction, tc.want)
			}
		})
	}
}
