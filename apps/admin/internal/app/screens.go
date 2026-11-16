package app

import (
	"context"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
)

type hookedScreen struct {
	shell.Screen
	beforeShow func(ctx context.Context, p model.Params) bool
}

func (h hookedScreen) Show(ctx context.Context, p model.Params) {
	if h.beforeShow != nil && !h.beforeShow(ctx, p) {
		return
	}
	h.Screen.Show(ctx, p)
}

type clipboardAdapter struct {
	app fyne.App
}

func (c clipboardAdapter) SetText(text string) { c.app.Clipboard().SetContent(text) }

func (c clipboardAdapter) Text() string { return c.app.Clipboard().Content() }

type closeGuardWindow struct {
	fyne.Window
	mu        sync.Mutex
	intercept func()
}

func newCloseGuardWindow(w fyne.Window) *closeGuardWindow {
	return &closeGuardWindow{Window: w}
}

func (g *closeGuardWindow) SetCloseIntercept(fn func()) {
	g.mu.Lock()
	g.intercept = fn
	g.mu.Unlock()
	g.Window.SetCloseIntercept(fn)
}

func (g *closeGuardWindow) requestClose() {
	g.mu.Lock()
	fn := g.intercept
	g.mu.Unlock()
	if fn == nil {
		g.Window.Close()
		return
	}
	fn()
}

func (g *closeGuardWindow) bindQuitShortcut() {
	shortcut := &desktop.CustomShortcut{KeyName: fyne.KeyQ, Modifier: fyne.KeyModifierShortcutDefault}
	g.Window.Canvas().AddShortcut(shortcut, func(fyne.Shortcut) { g.requestClose() })
}
