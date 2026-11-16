package vm

import (
	"sync"
	"time"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

const ClipboardClearAfter = 60 * time.Second

type Scheduler func(after time.Duration, run func()) (stop func())

type ClipboardGuard struct {
	inner    model.Clipboard
	schedule Scheduler
	after    time.Duration

	mu    sync.Mutex
	put   string
	armed bool
	stop  func()
}

func NewClipboardGuard(inner model.Clipboard, schedule Scheduler, after time.Duration) *ClipboardGuard {
	return &ClipboardGuard{inner: inner, schedule: schedule, after: after}
}

func (g *ClipboardGuard) SetText(text string) {
	g.inner.SetText(text)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cancelLocked()
	g.put, g.armed = text, true
	g.stop = g.schedule(g.after, g.clearIfUnchanged)
}

func (g *ClipboardGuard) Text() string {
	return g.inner.Text()
}

func (g *ClipboardGuard) clearIfUnchanged() {
	g.mu.Lock()
	put := g.put
	g.put, g.armed, g.stop = "", false, nil
	g.mu.Unlock()
	if put != "" && g.inner.Text() == put {
		g.inner.SetText("")
	}
}

func (g *ClipboardGuard) cancelLocked() {
	if g.stop != nil {
		g.stop()
		g.stop = nil
	}
}
