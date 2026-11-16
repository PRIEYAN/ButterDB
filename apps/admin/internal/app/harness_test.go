package app

import (
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
	"github.com/zalando/go-keyring"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/connect/credstore"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/connect/profilestore"
)

var fixedNow = time.Date(2026, 11, 16, 9, 30, 0, 0, time.UTC)

const waitLimit = 15 * time.Second

type uiQueue struct {
	mu    sync.Mutex
	tasks []func()
}

func (q *uiQueue) push(f func()) {
	q.mu.Lock()
	q.tasks = append(q.tasks, f)
	q.mu.Unlock()
}

func (q *uiQueue) pop() func() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.tasks) == 0 {
		return nil
	}
	task := q.tasks[0]
	q.tasks = q.tasks[1:]
	return task
}

type queuedDriver struct {
	fyne.Driver
	queue *uiQueue
}

func (d queuedDriver) DoFromGoroutine(f func(), _ bool) { d.queue.push(f) }

type queuedApp struct {
	fyne.App
	driver fyne.Driver
}

func (a queuedApp) Driver() fyne.Driver { return a.driver }

type harness struct {
	t         *testing.T
	queue     *uiQueue
	app       fyne.App
	win       fyne.Window
	comp      *composition
	opts      Options
	configDir string
	store     *profilestore.Store
	files     *credstore.File
	closed    bool
}

type harnessOption func(*harness)

func newHarness(t *testing.T, mutate ...func(*Options)) *harness {
	t.Helper()
	keyring.MockInit()
	base := test.NewTempApp(t)
	queue := &uiQueue{}
	wrapped := queuedApp{App: base, driver: queuedDriver{Driver: base.Driver(), queue: queue}}
	fyne.SetCurrentApp(wrapped)
	configDir := t.TempDir()
	store, err := profilestore.New(profilestore.PathIn(configDir))
	if err != nil {
		t.Fatalf("profile store: %v", err)
	}
	files, err := credstore.NewFile(credstore.TokenDirIn(configDir))
	if err != nil {
		t.Fatalf("file store: %v", err)
	}
	h := &harness{t: t, queue: queue, app: wrapped, configDir: configDir, store: store, files: files}
	h.opts = Options{
		Version:     "0.1.0",
		Now:         func() time.Time { return fixedNow },
		Dialer:      NewDialer(),
		Tokens:      credstore.NewKeychain(),
		Store:       store,
		Files:       files,
		Memory:      credstore.NewMemory(),
		DeviceName:  "Test laptop",
		DownloadDir: t.TempDir(),
	}
	for _, m := range mutate {
		m(&h.opts)
	}
	return h
}

func (h *harness) start() {
	h.t.Helper()
	h.win = h.app.NewWindow("test")
	comp, err := compose(h.app, h.win, h.opts)
	if err != nil {
		h.t.Fatalf("compose: %v", err)
	}
	h.comp = comp
	h.t.Cleanup(func() {
		comp.shutdown()
		h.drain()
	})
	h.pump()
}

func (h *harness) pump() {
	for task := h.queue.pop(); task != nil; task = h.queue.pop() {
		task()
	}
}

func (h *harness) drain() {
	quiet := 0
	deadline := time.Now().Add(5 * time.Second)
	for quiet < 15 && time.Now().Before(deadline) {
		if task := h.queue.pop(); task != nil {
			task()
			quiet = 0
			continue
		}
		quiet++
		time.Sleep(10 * time.Millisecond)
	}
}

func (h *harness) waitFor(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(waitLimit)
	for {
		h.pump()
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (h *harness) settle() {
	for i := 0; i < 5; i++ {
		h.pump()
		time.Sleep(5 * time.Millisecond)
	}
	h.pump()
}

func (h *harness) route() model.Route {
	return h.comp.sh.Navigator().Current()
}

func (h *harness) waitRoute(route model.Route) {
	h.t.Helper()
	h.waitFor("route "+string(route), func() bool { return h.route() == route })
}

func (h *harness) content() fyne.CanvasObject {
	return h.win.Canvas().Content()
}

func (h *harness) overlayObjects() []fyne.CanvasObject {
	top := h.win.Canvas().Overlays().Top()
	if top == nil {
		return nil
	}
	return visibleObjects(top)
}

func buttonText(o fyne.CanvasObject) (string, bool) {
	if _, ok := o.(fyne.Tappable); !ok {
		return "", false
	}
	v := reflect.Indirect(reflect.ValueOf(o))
	if v.Kind() != reflect.Struct {
		return "", false
	}
	field := v.FieldByName("Text")
	if !field.IsValid() || field.Kind() != reflect.String {
		return "", false
	}
	return field.String(), true
}

func findButton(objects []fyne.CanvasObject, text string) fyne.CanvasObject {
	for _, o := range objects {
		if got, ok := buttonText(o); ok && got == text {
			return o
		}
	}
	return nil
}

func (h *harness) button(text string) fyne.CanvasObject {
	h.t.Helper()
	b := findButton(visibleObjects(h.content()), text)
	if b == nil {
		h.t.Fatalf("button %q not on screen", text)
	}
	return b
}

func (h *harness) tapButton(text string) {
	h.t.Helper()
	test.Tap(h.button(text).(fyne.Tappable))
}

func (h *harness) entry(placeholder string) *widget.Entry {
	h.t.Helper()
	for _, o := range visibleObjects(h.content()) {
		if e, ok := o.(*widget.Entry); ok && e.PlaceHolder == placeholder {
			return e
		}
	}
	h.t.Fatalf("entry %q not on screen", placeholder)
	return nil
}

func (h *harness) onlyVisibleEntry() *widget.Entry {
	h.t.Helper()
	var found *widget.Entry
	for _, o := range visibleObjects(h.content()) {
		if e, ok := o.(*widget.Entry); ok {
			if found != nil {
				h.t.Fatalf("more than one entry on screen")
			}
			found = e
		}
	}
	if found == nil {
		h.t.Fatalf("no entry on screen")
	}
	return found
}

func (h *harness) hasLabel(substr string) bool {
	for _, o := range visibleObjects(h.content()) {
		if l, ok := o.(*widget.Label); ok && strings.Contains(l.Text, substr) {
			return true
		}
	}
	return false
}

func visibleObjects(root fyne.CanvasObject) []fyne.CanvasObject {
	if root == nil || !root.Visible() {
		return nil
	}
	out := []fyne.CanvasObject{root}
	switch c := root.(type) {
	case fyne.Widget:
		for _, child := range test.WidgetRenderer(c).Objects() {
			out = append(out, visibleObjects(child)...)
		}
	case *fyne.Container:
		for _, child := range c.Objects {
			out = append(out, visibleObjects(child)...)
		}
	}
	return out
}

func (h *harness) typeShortcut(key fyne.KeyName) {
	h.t.Helper()
	handler, ok := h.win.Canvas().(interface{ TypedShortcut(fyne.Shortcut) })
	if !ok {
		h.t.Fatalf("canvas cannot take shortcuts")
	}
	handler.TypedShortcut(&desktop.CustomShortcut{KeyName: key, Modifier: fyne.KeyModifierShortcutDefault})
}

func (h *harness) seedProfile(admin *fakeAdmin, kind model.TokenStorage) model.Profile {
	h.t.Helper()
	profile := model.Profile{
		ServerID:      testServerID,
		Label:         "home server",
		BaseURL:       admin.srv.URL,
		Trust:         adminv1Pinned(admin.pin),
		DeviceID:      testDeviceID,
		DeviceName:    "Test laptop",
		TokenStorage:  kind,
		TokenIssuedAt: fixedNow,
		AddedAt:       fixedNow,
		LastUsedAt:    fixedNow,
	}
	if err := h.store.Put(profile); err != nil {
		h.t.Fatalf("seed profile: %v", err)
	}
	if err := h.store.SetLastUsed(profile.ServerID); err != nil {
		h.t.Fatalf("seed last used: %v", err)
	}
	if err := h.opts.tokenStoreFor(kind).Set(profile.ServerID, testDeviceTok); err != nil {
		h.t.Fatalf("seed token: %v", err)
	}
	return profile
}

func adminv1Pinned(pin string) adminv1.Trust {
	return adminv1.Trust{Mode: adminv1.TrustPinned, SPKISHA256: pin}
}

func reflectType(v any) string { return reflect.TypeOf(v).String() }

func (h *harness) waitVisible(what string, find func() fyne.CanvasObject) fyne.CanvasObject {
	h.t.Helper()
	var found fyne.CanvasObject
	h.waitFor(what, func() bool {
		found = find()
		return found != nil
	})
	return found
}

func (h *harness) waitButton(text string) fyne.Tappable {
	h.t.Helper()
	o := h.waitVisible("button "+text, func() fyne.CanvasObject { return findButton(visibleObjects(h.content()), text) })
	return o.(fyne.Tappable)
}

func (h *harness) waitEntry(placeholder string) *widget.Entry {
	h.t.Helper()
	o := h.waitVisible("entry "+placeholder, func() fyne.CanvasObject {
		for _, o := range visibleObjects(h.content()) {
			if e, ok := o.(*widget.Entry); ok && e.PlaceHolder == placeholder {
				return e
			}
		}
		return nil
	})
	return o.(*widget.Entry)
}

func (h *harness) waitLabel(substr string) {
	h.t.Helper()
	h.waitFor("label "+substr, func() bool { return h.hasLabel(substr) })
}

func (h *harness) waitOverlayButton(text string) fyne.Tappable {
	h.t.Helper()
	o := h.waitVisible("dialog button "+text, func() fyne.CanvasObject { return findButton(h.overlayObjects(), text) })
	return o.(fyne.Tappable)
}

func (h *harness) waitOverlayEntry(placeholder string) *widget.Entry {
	h.t.Helper()
	o := h.waitVisible("dialog entry "+placeholder, func() fyne.CanvasObject {
		for _, o := range h.overlayObjects() {
			if e, ok := o.(*widget.Entry); ok && e.PlaceHolder == placeholder {
				return e
			}
		}
		return nil
	})
	return o.(*widget.Entry)
}

func (h *harness) waitOverlayCheck(text string) *widget.Check {
	h.t.Helper()
	o := h.waitVisible("dialog check "+text, func() fyne.CanvasObject {
		for _, o := range h.overlayObjects() {
			if c, ok := o.(*widget.Check); ok && c.Text == text {
				return c
			}
		}
		return nil
	})
	return o.(*widget.Check)
}

func (h *harness) waitNoOverlay() {
	h.t.Helper()
	h.waitFor("dialogs closed", func() bool { return h.win.Canvas().Overlays().Top() == nil })
}

func (h *harness) session() *session {
	h.t.Helper()
	s := h.comp.currentSession()
	if s == nil {
		h.t.Fatalf("no active session")
	}
	return s
}
