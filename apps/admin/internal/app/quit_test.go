package app

import (
	"os"
	"path/filepath"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	objectsvm "github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/objects/vm"
)

func (h *harness) appClosed() bool {
	select {
	case <-h.comp.ctx.Done():
		return true
	default:
		return false
	}
}

func TestQuitShortcutClosesTheWindowWithoutSession(t *testing.T) {
	h := newHarness(t)
	h.start()
	h.waitRoute(model.RouteTelegram)

	h.typeShortcut(fyne.KeyQ)

	h.waitFor("window closed", h.appClosed)
}

func TestQuitShortcutClosesTheWindowWhenNoTransferRuns(t *testing.T) {
	h := newHarness(t, withLinked())
	h.startInProject("photos")

	h.typeShortcut(fyne.KeyQ)

	h.waitFor("window closed", h.appClosed)
}

func TestQuitShortcutAsksBeforeDroppingRunningTransfers(t *testing.T) {
	h := newHarness(t, withLinked())
	admin := h.admin
	admin.seedBucket("photos")
	admin.putGate = make(chan struct{})
	admin.putStarted = make(chan struct{}, 1)
	h.startInProject("photos")
	h.browseBucket("photos")

	models := h.session().models
	path := filepath.Join(t.TempDir(), "big.bin")
	if err := os.WriteFile(path, make([]byte, 64*1024), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	spec, err := models.objects.PrepareUpload("", path)
	if err != nil {
		t.Fatalf("prepare upload: %v", err)
	}
	finished := make(chan struct{})
	ctx := h.session().ctx
	go func() {
		defer close(finished)
		models.objects.Upload(ctx, "photos", []objectsvm.UploadSpec{spec}, objectsvm.Prompts{})
	}()
	released := false
	release := func() {
		if !released {
			released = true
			close(admin.putGate)
		}
	}
	defer func() {
		release()
		h.waitFor("upload goroutine ended", func() bool {
			select {
			case <-finished:
				return true
			default:
				return false
			}
		})
	}()
	h.waitFor("upload running", func() bool { return models.objects.ActiveTransfers() == 1 })
	<-admin.putStarted

	h.typeShortcut(fyne.KeyQ)
	keep := h.waitOverlayButton("Keep open")
	if h.appClosed() {
		t.Fatalf("Mod+Q closed the app while a transfer was running")
	}
	test.Tap(keep)
	h.waitNoOverlay()
	if h.appClosed() {
		t.Fatalf("Keep open closed the app")
	}

	h.typeShortcut(fyne.KeyQ)
	test.Tap(h.waitOverlayButton("Quit anyway"))
	h.waitFor("window closed", h.appClosed)
}
