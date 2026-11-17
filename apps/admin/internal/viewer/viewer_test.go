package viewer

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"image"
	"image/png"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/driver"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
)

type testWindows struct {
	app fyne.App
	win *nativeWindow
}

func (f *testWindows) NewWindow(title string) fyne.Window {
	f.win = &nativeWindow{Window: f.app.NewWindow(title), closed: make(chan struct{})}
	return f.win
}

type nativeWindow struct {
	fyne.Window
	closed chan struct{}
	once   sync.Once
}

func (w *nativeWindow) RunNative(fn func(any)) {
	fn(driver.X11WindowContext{WindowHandle: 42})
}

func (w *nativeWindow) Close() {
	w.once.Do(func() { w.Window.Close(); close(w.closed) })
}

type playback struct {
	window uintptr
	url    string
	data   string
}

type testPlayer struct {
	started chan playback
	stopped chan struct{}
}

func (p *testPlayer) Play(ctx context.Context, window uintptr, url string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	p.started <- playback{window, url, string(data)}
	<-ctx.Done()
	close(p.stopped)
	return ctx.Err()
}

func viewerFixture(t *testing.T, source *memorySource) (*Viewer, *testWindows, chan func(), *testPlayer) {
	t.Helper()
	windows := &testWindows{app: test.NewTempApp(t)}
	player := &testPlayer{started: make(chan playback, 1), stopped: make(chan struct{})}
	v, err := New(Dependencies{Windows: windows, Source: source, Player: player, Random: rand.Reader})
	if err != nil {
		t.Fatal(err)
	}
	queue := make(chan func(), 20)
	v.post = func(fn func()) { queue <- fn }
	t.Cleanup(func() {
		if windows.win != nil {
			windows.win.Close()
		}
	})
	return v, windows, queue, player
}

func runUI(t *testing.T, queue chan func()) {
	t.Helper()
	select {
	case fn := <-queue:
		fn()
	case <-time.After(5 * time.Second):
		t.Fatal("viewer did not finish loading")
	}
}

func TestViewerRendersImage(t *testing.T) {
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 8, 6))); err != nil {
		t.Fatal(err)
	}
	source := &memorySource{meta: adminv1.ObjectMeta{Key: "cat.png", ContentType: "image/png", SizeBytes: int64(data.Len())}, data: data.Bytes()}
	v, windows, queue, _ := viewerFixture(t, source)
	v.Open(context.Background(), "photos", adminv1.ObjectSummary{Key: "cat.png"})
	runUI(t, queue)
	for _, object := range test.LaidOutObjects(windows.win.Content()) {
		if img, ok := object.(*canvas.Image); ok && img.Image != nil && img.Image.Bounds().Dx() == 8 && img.Image.Bounds().Dy() == 6 {
			return
		}
	}
	t.Fatal("viewer did not render the image")
}

func TestViewerShowsGenericFileWithoutReadingItsBytes(t *testing.T) {
	source := &memorySource{meta: adminv1.ObjectMeta{Key: "docs/archive.zip", ContentType: "application/zip", SizeBytes: 2048}}
	v, windows, queue, _ := viewerFixture(t, source)
	v.Open(context.Background(), "photos", adminv1.ObjectSummary{Key: "docs/archive.zip"})
	runUI(t, queue)
	if !hasLabel(windows.win.Content(), "archive.zip") || !hasLabel(windows.win.Content(), "application/zip") {
		t.Fatal("generic file card has no readable filename and type")
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if len(source.opts) != 0 {
		t.Fatal("generic file card read file bytes")
	}
}

func TestViewerStreamsMediaAndStopsOnClose(t *testing.T) {
	for _, mime := range []string{"video/mp4", "audio/mpeg"} {
		t.Run(mime, func(t *testing.T) {
			source := &memorySource{meta: adminv1.ObjectMeta{Key: "media", ContentType: mime, SizeBytes: 5}, data: []byte("media")}
			v, windows, queue, player := viewerFixture(t, source)
			v.Open(context.Background(), "photos", adminv1.ObjectSummary{Key: "media"})
			runUI(t, queue)
			var play playback
			select {
			case play = <-player.started:
			case <-time.After(5 * time.Second):
				t.Fatal("player did not start")
			}
			if play.window != 42 || play.data != "media" {
				t.Fatalf("playback = %+v", play)
			}
			windows.win.Close()
			select {
			case <-player.stopped:
			case <-time.After(5 * time.Second):
				t.Fatal("closing the viewer did not stop playback")
			}
		})
	}
}

func TestViewerRejectsMalformedAndOversizedImages(t *testing.T) {
	for _, tc := range []struct {
		name string
		size int64
		data []byte
	}{{"malformed", 3, []byte("bad")}, {"oversized", 21 * 1024 * 1024, nil}} {
		t.Run(tc.name, func(t *testing.T) {
			source := &memorySource{meta: adminv1.ObjectMeta{Key: "bad.png", ContentType: "image/png", SizeBytes: tc.size}, data: tc.data}
			v, windows, queue, _ := viewerFixture(t, source)
			v.Open(context.Background(), "photos", adminv1.ObjectSummary{Key: "bad.png"})
			runUI(t, queue)
			if !hasLabel(windows.win.Content(), "Cannot open file") {
				t.Fatal("failed image did not show an error")
			}
		})
	}
}

func TestViewerParentCancellationClosesWindow(t *testing.T) {
	source := &memorySource{meta: adminv1.ObjectMeta{Key: "file"}}
	v, windows, queue, _ := viewerFixture(t, source)
	ctx, cancel := context.WithCancel(context.Background())
	v.Open(ctx, "photos", adminv1.ObjectSummary{Key: "file"})
	cancel()
	for i := 0; i < 2; i++ {
		runUI(t, queue)
		select {
		case <-windows.win.closed:
			return
		default:
		}
	}
	t.Fatal("cancelling the page did not close its viewer")
}

func TestViewerMetadataError(t *testing.T) {
	source := &memorySource{metaErr: errors.New("private upstream details")}
	v, windows, queue, _ := viewerFixture(t, source)
	v.Open(context.Background(), "photos", adminv1.ObjectSummary{Key: "file"})
	runUI(t, queue)
	if !hasLabel(windows.win.Content(), "Cannot open file") || hasLabel(windows.win.Content(), "private upstream details") {
		t.Fatal("metadata failure must show a readable error")
	}
}

func hasLabel(root fyne.CanvasObject, want string) bool {
	for _, object := range test.LaidOutObjects(root) {
		if label, ok := object.(*widget.Label); ok && strings.Contains(label.Text, want) {
			return true
		}
	}
	return false
}
