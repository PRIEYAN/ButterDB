package shell

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

const (
	cancelLabel        = "Cancel"
	confirmDialogLabel = "confirm/destructive"
	typedDialogLabel   = "confirm/typed"
	problemDialogLabel = "notice"
)

type keyButton struct {
	widget.Button
	onEscape func()
}

func newKeyButton(label string, icon fyne.Resource, onTapped, onEscape func()) *keyButton {
	b := &keyButton{onEscape: onEscape}
	b.Text = label
	b.Icon = icon
	b.OnTapped = onTapped
	b.ExtendBaseWidget(b)
	return b
}

func (b *keyButton) TypedKey(ev *fyne.KeyEvent) {
	switch ev.Name {
	case fyne.KeyEscape:
		b.onEscape()
	case fyne.KeyReturn, fyne.KeyEnter:
		b.Tapped(nil)
	default:
		b.Button.TypedKey(ev)
	}
}

type escapeEntry struct {
	widget.Entry
	onEscape func()
}

func newEscapeEntry(onEscape func()) *escapeEntry {
	e := &escapeEntry{onEscape: onEscape}
	e.ExtendBaseWidget(e)
	return e
}

func (e *escapeEntry) TypedKey(ev *fyne.KeyEvent) {
	if ev.Name == fyne.KeyEscape {
		e.onEscape()
		return
	}
	e.Entry.TypedKey(ev)
}

type confirmDialog struct {
	dlg     *Dialog
	win     fyne.Window
	cancel  *keyButton
	confirm *keyButton
	entry   *escapeEntry
}

func (c *confirmDialog) show() {
	c.dlg.Show()
	if c.entry != nil {
		c.win.Canvas().Focus(c.entry)
		return
	}
	c.win.Canvas().Focus(c.cancel)
}

func newConfirmDialog(win fyne.Window, label, title string, content fyne.CanvasObject, confirmLabel string, onConfirm func()) *confirmDialog {
	c := &confirmDialog{win: win}
	c.dlg = NewDialog(win, label, title, content)
	dismiss := func() { c.dlg.Hide() }
	c.cancel = newKeyButton(cancelLabel, theme.CancelIcon(), dismiss, dismiss)
	c.confirm = newKeyButton(confirmLabel, theme.DeleteIcon(), func() {
		c.dlg.Hide()
		onConfirm()
	}, dismiss)
	c.confirm.Importance = widget.DangerImportance
	c.dlg.SetButtons(c.cancel, c.confirm)
	return c
}

func newTypedConfirm(win fyne.Window, title, body, mustType, confirmLabel string, onConfirm func()) *confirmDialog {
	entry := newEscapeEntry(nil)
	entry.SetPlaceHolder(`Type "` + mustType + `" to confirm`)
	content := container.NewVBox(NewCalloutText(CalloutDanger, body), widget.NewLabel(`Type "`+mustType+`" to confirm.`), entry)
	c := newConfirmDialog(win, typedDialogLabel, title, content, confirmLabel, onConfirm)
	c.entry = entry
	entry.onEscape = func() { c.dlg.Hide() }
	c.confirm.Disable()
	entry.OnChanged = func(text string) {
		if text == mustType {
			c.confirm.Enable()
			return
		}
		c.confirm.Disable()
	}
	return c
}

func ConfirmTyped(win fyne.Window, title, body, mustType, confirmLabel string, onConfirm func()) {
	newTypedConfirm(win, title, body, mustType, confirmLabel, onConfirm).show()
}

func newDestructiveConfirm(win fyne.Window, title, body, confirmLabel string, onConfirm func()) *confirmDialog {
	return newConfirmDialog(win, confirmDialogLabel, title, NewCalloutText(CalloutDanger, body), confirmLabel, onConfirm)
}

func ConfirmDestructive(win fyne.Window, title, body, confirmLabel string, onConfirm func()) {
	newDestructiveConfirm(win, title, body, confirmLabel, onConfirm).show()
}

func showProblemDialog(win fyne.Window, content fyne.CanvasObject) *keyButton {
	dlg := NewDialog(win, problemDialogLabel, "", content)
	dismiss := func() { dlg.Hide() }
	closeButton := newKeyButton(closeLabel, theme.CancelIcon(), dismiss, dismiss)
	dlg.SetButtons(closeButton)
	dlg.Show()
	win.Canvas().Focus(closeButton)
	return closeButton
}
