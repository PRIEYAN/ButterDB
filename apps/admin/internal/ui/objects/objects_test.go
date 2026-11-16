package objects

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/objects/vm"
)

type fakeClient struct {
	mu          sync.Mutex
	pages       map[string]adminv1.ObjectList
	buckets     []adminv1.Bucket
	puts        []string
	putOpts     []adminv1.PutObjectOptions
	deleteSolo  []string
	deleteBatch [][]string
	metas       map[string]adminv1.ObjectMeta
	listPrefix  []string
	streams     map[string][]byte
}

func newFakeClient() *fakeClient {
	return &fakeClient{pages: map[string]adminv1.ObjectList{}, metas: map[string]adminv1.ObjectMeta{}, streams: map[string][]byte{}}
}

func (f *fakeClient) ListBuckets(context.Context) (adminv1.BucketList, error) {
	return adminv1.BucketList{Buckets: f.buckets}, nil
}

func (f *fakeClient) ListObjects(_ context.Context, _ string, opts adminv1.ListObjectsOptions) (adminv1.ObjectList, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listPrefix = append(f.listPrefix, opts.Prefix)
	return f.pages[opts.Prefix], nil
}

func (f *fakeClient) HeadObject(context.Context, string, string) (adminv1.ObjectMeta, error) {
	return adminv1.ObjectMeta{}, &adminv1.APIError{Status: http.StatusNotFound, Body: adminv1.ErrorBody{Code: adminv1.CodeObjectNotFound}}
}

func (f *fakeClient) ObjectMeta(_ context.Context, _, key string) (adminv1.ObjectMeta, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if meta, ok := f.metas[key]; ok {
		return meta, nil
	}
	return adminv1.ObjectMeta{Key: key, ContentType: "text/plain"}, nil
}

func (f *fakeClient) GetObject(_ context.Context, _, key string, _ adminv1.GetObjectOptions) (adminv1.ObjectStream, error) {
	f.mu.Lock()
	data, ok := f.streams[key]
	f.mu.Unlock()
	if ok {
		return adminv1.ObjectStream{Body: io.NopCloser(bytes.NewReader(data)), Status: http.StatusOK, ContentLength: int64(len(data))}, nil
	}
	return adminv1.ObjectStream{}, &adminv1.APIError{Status: http.StatusNotFound, Body: adminv1.ErrorBody{Code: adminv1.CodeObjectNotFound}}
}

func (f *fakeClient) PutObject(_ context.Context, _, key string, body io.Reader, _ int64, opts adminv1.PutObjectOptions) (adminv1.ObjectMeta, error) {
	_, _ = io.Copy(io.Discard, body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts = append(f.puts, key)
	f.putOpts = append(f.putOpts, opts)
	return adminv1.ObjectMeta{Key: key}, nil
}

func (f *fakeClient) DeleteObject(_ context.Context, _, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteSolo = append(f.deleteSolo, key)
	return nil
}

func (f *fakeClient) DeleteObjects(_ context.Context, _ string, req adminv1.DeleteObjectsRequest) (adminv1.DeleteObjectsResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteBatch = append(f.deleteBatch, req.Keys)
	return adminv1.DeleteObjectsResult{Deleted: req.Keys}, nil
}

func (f *fakeClient) Presign(context.Context, string, adminv1.PresignRequest) (adminv1.PresignedURL, error) {
	return adminv1.PresignedURL{}, nil
}

func (f *fakeClient) ListAccessKeys(context.Context, bool) (adminv1.AccessKeyList, error) {
	return adminv1.AccessKeyList{}, nil
}

func (f *fakeClient) Status(context.Context) (adminv1.Status, error) {
	return adminv1.Status{Limits: adminv1.Limits{MaxObjectBytes: 1 << 20, MaxKeyBytes: 1024, MaxUserMetadataBytes: 2048}}, nil
}

func (f *fakeClient) putKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.puts...)
}

func (f *fakeClient) deletes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.deleteSolo) + len(f.deleteBatch)
}

type memoryClipboard struct{ text string }

func (c *memoryClipboard) SetText(text string) { c.text = text }

func (c *memoryClipboard) Text() string { return c.text }

type recordingNavigator struct {
	mu     sync.Mutex
	routes []model.Route
}

func (n *recordingNavigator) Navigate(r model.Route, _ model.Params) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.routes = append(n.routes, r)
}

func (n *recordingNavigator) Current() model.Route { return model.RouteObjects }

func (n *recordingNavigator) SetEnabled(model.Route, bool, string) {}

type harness struct {
	client  *fakeClient
	vmodel  *vm.ViewModel
	browser *browser
	win     fyne.Window
}

func newHarness(t *testing.T, client *fakeClient) *harness {
	t.Helper()
	test.NewTempApp(t)
	win := test.NewTempWindow(t, widget.NewLabel(""))
	v := vm.New(client, func() time.Time { return time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC) })
	b := newBrowser(v, &recordingNavigator{}, win, Options{Clipboard: &memoryClipboard{}})
	win.SetContent(b.root)
	win.Resize(fyne.NewSize(1200, 700))
	h := &harness{client: client, vmodel: v, browser: b, win: win}
	t.Cleanup(func() { h.quiesce(t) })
	return h
}

func (h *harness) quiesce(t *testing.T) {
	eventually(t, "transfers to finish", func() bool { return h.vmodel.ActiveTransfers() == 0 })
	h.vmodel.Location()
	h.browser.uiMu.Lock()
	h.browser.uiMu.Unlock()
	time.Sleep(10 * time.Millisecond)
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func writeFile(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDropUploadsUnderPrefix(t *testing.T) {
	h := newHarness(t, newFakeClient())
	h.vmodel.Open(context.Background(), "photos", "2026/11/")
	first := writeFile(t, "cat.jpg")
	second := writeFile(t, "dog.png")
	h.browser.handleDrop([]fyne.URI{storage.NewFileURI(first), storage.NewFileURI(second)})
	eventually(t, "both dropped files uploaded", func() bool { return len(h.client.putKeys()) == 2 })
	got := h.client.putKeys()
	want := map[string]bool{"2026/11/cat.jpg": true, "2026/11/dog.png": true}
	for _, key := range got {
		if !want[key] {
			t.Fatalf("unexpected upload key %q in %v", key, got)
		}
	}
	for _, opts := range h.client.putOpts {
		if !opts.IfNoneMatchAny || opts.IdempotencyKey == "" {
			t.Fatalf("drop upload options = %+v", opts)
		}
	}
}

func TestDropWithoutBucketUploadsNothing(t *testing.T) {
	h := newHarness(t, newFakeClient())
	h.browser.handleDrop([]fyne.URI{storage.NewFileURI(writeFile(t, "a.txt"))})
	time.Sleep(20 * time.Millisecond)
	if len(h.client.putKeys()) != 0 {
		t.Fatal("upload started with no bucket chosen")
	}
}

func TestBackspaceGoesUp(t *testing.T) {
	client := newFakeClient()
	h := newHarness(t, client)
	h.vmodel.Open(context.Background(), "photos", "a/b/")
	h.win.Canvas().Focus(h.browser.table)
	h.browser.table.TypedKey(&fyne.KeyEvent{Name: fyne.KeyBackspace})
	eventually(t, "prefix moved up", func() bool {
		_, prefix := h.vmodel.Location()
		return prefix == "a/"
	})
	h.browser.table.TypedKey(&fyne.KeyEvent{Name: fyne.KeyBackspace})
	eventually(t, "prefix at root", func() bool {
		_, prefix := h.vmodel.Location()
		return prefix == ""
	})
	h.browser.table.TypedKey(&fyne.KeyEvent{Name: fyne.KeyBackspace})
	time.Sleep(20 * time.Millisecond)
	if _, prefix := h.vmodel.Location(); prefix != "" {
		t.Fatalf("prefix = %q after Up at root", prefix)
	}
}

func TestDeleteKeyAsksConfirmation(t *testing.T) {
	client := newFakeClient()
	client.pages[""] = adminv1.ObjectList{Objects: []adminv1.ObjectSummary{{Key: "a.txt"}, {Key: "b.txt"}}}
	h := newHarness(t, client)
	h.vmodel.Open(context.Background(), "photos", "")
	h.vmodel.ToggleObject("a.txt")
	h.vmodel.ToggleObject("b.txt")
	h.win.Canvas().Focus(h.browser.table)
	h.browser.table.TypedKey(&fyne.KeyEvent{Name: fyne.KeyDelete})
	if h.win.Canvas().Overlays().Top() == nil {
		t.Fatal("Delete key showed no confirmation dialog")
	}
	time.Sleep(20 * time.Millisecond)
	if client.deletes() != 0 {
		t.Fatal("objects were deleted before the user confirmed")
	}
}

func TestDeleteConfirmationDeletesAfterConfirm(t *testing.T) {
	client := newFakeClient()
	client.pages[""] = adminv1.ObjectList{Objects: []adminv1.ObjectSummary{{Key: "a.txt"}}}
	h := newHarness(t, client)
	h.vmodel.Open(context.Background(), "photos", "")
	h.vmodel.ToggleObject("a.txt")
	h.browser.requestDelete()
	overlay := h.win.Canvas().Overlays().Top()
	if overlay == nil {
		t.Fatal("no confirmation dialog")
	}
	confirm := findButton(overlay, "Delete")
	if confirm == nil {
		t.Fatal("confirmation dialog has no Delete button")
	}
	test.Tap(confirm)
	eventually(t, "single object deleted", func() bool { return client.deletes() == 1 })
}

func findButton(root fyne.CanvasObject, label string) fyne.Tappable {
	for _, o := range test.LaidOutObjects(root) {
		if buttonText(o) == label {
			if tappable, ok := o.(fyne.Tappable); ok {
				return tappable
			}
		}
	}
	return nil
}

func buttonText(o fyne.CanvasObject) string {
	if b, ok := o.(*widget.Button); ok {
		return b.Text
	}
	value := reflect.ValueOf(o)
	if value.Kind() != reflect.Pointer || value.Elem().Kind() != reflect.Struct {
		return ""
	}
	embedded := value.Elem().FieldByName("Button")
	if !embedded.IsValid() || embedded.Type() != reflect.TypeOf(widget.Button{}) {
		return ""
	}
	return embedded.FieldByName("Text").String()
}

func TestPresignDialogOpensForSelectedObject(t *testing.T) {
	client := newFakeClient()
	client.pages[""] = adminv1.ObjectList{Objects: []adminv1.ObjectSummary{{Key: "a.txt"}}}
	h := newHarness(t, client)
	h.vmodel.Open(context.Background(), "photos", "")
	h.vmodel.ToggleObject("a.txt")
	h.browser.openPresignDialog()
	overlay := h.win.Canvas().Overlays().Top()
	if overlay == nil {
		t.Fatal("presign dialog did not open")
	}
	if findButton(overlay, "Generate") == nil {
		t.Fatal("presign dialog has no Generate button")
	}
}

func TestSelectAllShortcutSelectsLoadedRows(t *testing.T) {
	client := newFakeClient()
	client.pages[""] = adminv1.ObjectList{Objects: []adminv1.ObjectSummary{{Key: "a"}, {Key: "b"}}, CommonPrefixes: []string{"d/"}}
	h := newHarness(t, client)
	h.vmodel.Open(context.Background(), "photos", "")
	h.browser.table.TypedShortcut(&fyne.ShortcutSelectAll{})
	st := h.vmodel.State().Get()
	if len(st.SelectedObjects) != 2 || len(st.SelectedFolders) != 1 {
		t.Fatalf("selection = %v %v", st.SelectedObjects, st.SelectedFolders)
	}
}

func TestFolderRowActivationOpensIt(t *testing.T) {
	client := newFakeClient()
	client.pages[""] = adminv1.ObjectList{CommonPrefixes: []string{"docs/"}}
	h := newHarness(t, client)
	h.vmodel.Open(context.Background(), "photos", "")
	h.browser.activateRow(0)
	eventually(t, "folder opened", func() bool {
		_, prefix := h.vmodel.Location()
		return prefix == "docs/"
	})
}

func TestTransferStatusText(t *testing.T) {
	cases := []struct {
		name string
		item vm.Transfer
		want string
	}{
		{"queued", vm.Transfer{State: vm.TransferQueued}, "Waiting"},
		{"sending", vm.Transfer{State: vm.TransferSending, Sent: 1024, Total: 2048, BytesPerSecond: 512}, "1.0 KiB of 2.0 KiB · 512 B/s"},
		{"committing", vm.Transfer{State: vm.TransferCommitting}, "Saving to Telegram…"},
		{"checking", vm.Transfer{State: vm.TransferCommitting, Message: "Checking whether Telegram saved it…"}, "Checking whether Telegram saved it…"},
		{"uploaded", vm.Transfer{State: vm.TransferDone, Kind: vm.KindUpload, ETag: `"abc"`}, `Uploaded · "abc"`},
		{"downloaded", vm.Transfer{State: vm.TransferDone, Kind: vm.KindDownload}, "Downloaded"},
		{"failed", vm.Transfer{State: vm.TransferFailed, Problem: &model.Problem{Title: "Too large (max 1.0 MiB)."}}, "Too large (max 1.0 MiB)."},
		{"cancelled", vm.Transfer{State: vm.TransferCancelled}, "Cancelled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := TransferStatusText(tc.item); got != tc.want {
				t.Fatalf("TransferStatusText = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestScreenContract(t *testing.T) {
	test.NewTempApp(t)
	win := test.NewTempWindow(t, widget.NewLabel(""))
	client := newFakeClient()
	client.buckets = []adminv1.Bucket{{Name: "photos"}}
	v := vm.New(client, time.Now)
	screen := NewScreen(v, &recordingNavigator{}, win, Options{})
	if screen.Route() != model.RouteObjects || screen.Title() == "" || screen.Icon() == nil || screen.Shortcut() != fyne.Key4 || screen.Content() == nil {
		t.Fatalf("screen identity incomplete: %v %q", screen.Route(), screen.Title())
	}
	ctx, cancel := context.WithCancel(context.Background())
	screen.Show(ctx, model.Params{model.ParamBucket: "photos", model.ParamPrefix: "a/"})
	eventually(t, "bucket opened from params", func() bool {
		bucket, prefix := v.Location()
		return bucket == "photos" && prefix == "a/"
	})
	screen.Hide()
	cancel()
}

func smallPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(1, 1, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func oversizedGIFHeader() []byte {
	return []byte{'G', 'I', 'F', '8', '9', 'a', 0x41, 0x1f, 0x88, 0x13, 0, 0, 0}
}

func TestPreviewRendersImage(t *testing.T) {
	client := newFakeClient()
	client.pages[""] = adminv1.ObjectList{Objects: []adminv1.ObjectSummary{{Key: "cat.png", ContentType: "image/png", SizeBytes: 100}}}
	client.metas["cat.png"] = adminv1.ObjectMeta{Key: "cat.png", ContentType: "image/png", SizeBytes: 100, ETag: `"e1"`, Encrypted: true}
	client.streams["cat.png"] = smallPNG(t)
	h := newHarness(t, client)
	h.vmodel.Open(context.Background(), "photos", "")
	h.vmodel.Focus(context.Background(), "cat.png")
	h.browser.uiMu.Lock()
	h.browser.uiMu.Unlock()
	eventually(t, "decoded preview image", func() bool {
		h.browser.uiMu.Lock()
		defer h.browser.uiMu.Unlock()
		return h.browser.details.image.Image != nil && h.browser.details.image.Visible()
	})
	if h.browser.details.image.FillMode != canvas.ImageFillContain || h.browser.details.image.ScaleMode != canvas.ImageScaleSmooth {
		t.Fatal("preview image is not contain and smooth")
	}
}

func TestPreviewRefusesTooManyPixels(t *testing.T) {
	client := newFakeClient()
	client.metas["huge.gif"] = adminv1.ObjectMeta{Key: "huge.gif", ContentType: "image/gif", SizeBytes: int64(len(oversizedGIFHeader()))}
	client.streams["huge.gif"] = oversizedGIFHeader()
	h := newHarness(t, client)
	h.vmodel.Open(context.Background(), "photos", "")
	h.vmodel.Focus(context.Background(), "huge.gif")
	eventually(t, "pixel ceiling message", func() bool {
		return h.vmodel.State().Get().Preview.Message == previewTooManyPixel
	})
	h.browser.uiMu.Lock()
	defer h.browser.uiMu.Unlock()
	if h.browser.details.image.Image != nil && h.browser.details.image.Visible() {
		t.Fatal("an over-ceiling image was rendered")
	}
}

func TestDetailsListsMetaFields(t *testing.T) {
	meta := adminv1.ObjectMeta{Key: "a/b.txt", SizeBytes: 2048, StoredBytes: 2100, ContentType: "text/plain", ETag: `"e"`, SHA256: "abc", VersionID: "77", UserMetadata: map[string]string{"camera": "x100v"}}
	labels := map[string]string{}
	for _, field := range metaFields(meta) {
		labels[field.label] = field.value
	}
	for _, want := range []string{"Key", "Size", "Stored size", "Content type", "Last modified", "ETag", "SHA-256", "Encryption", "Version ID", "Replicated", "Metadata camera"} {
		if _, ok := labels[want]; !ok {
			t.Errorf("missing field %q", want)
		}
	}
	if labels["Encryption"] != "Not encrypted" || labels["Metadata camera"] != "x100v" || labels["Size"] != "2.0 KiB (2048 bytes)" {
		t.Fatalf("fields = %v", labels)
	}
}

func TestCloseGuardOffersKeepOpen(t *testing.T) {
	h := newHarness(t, newFakeClient())
	h.browser.askQuitWithTransfers(2)
	overlay := h.win.Canvas().Overlays().Top()
	if overlay == nil || findButton(overlay, keepOpenLabel) == nil || findButton(overlay, quitAnywayLabel) == nil {
		t.Fatal("close guard dialog lacks Keep open and Quit anyway")
	}
	test.Tap(findButton(overlay, keepOpenLabel))
}
