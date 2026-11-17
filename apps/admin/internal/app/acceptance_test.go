package app

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	bucketsvm "github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/buckets/vm"
	objectsvm "github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/objects/vm"
	telegramvm "github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/telegramlink/vm"
)

func (h *harness) linkThroughUI() {
	h.t.Helper()
	h.waitRoute(model.RouteTelegram)
	test.Type(h.waitEntry("digits only"), "123456")
	test.Type(h.waitEntry("32 hexadecimal characters"), testAPIHash)
	test.Tap(h.waitButton("Save app credentials"))
	test.Type(h.waitEntry("+447700900134"), "+447700900134")
	test.Tap(h.waitButton("Send code"))

	h.waitButton("Submit code")
	h.waitFor("code step entry", func() bool { return h.comp.telegram.State().Get().Step == telegramvm.StepCode })
	code := h.onlyVisibleEntry()
	test.Type(code, "00000")
	test.Tap(h.waitButton("Submit code"))
	h.waitFor("wrong code answered", func() bool { return h.admin.callCount("POST", adminv1.PathSetupTelegramCode) == 1 })
	h.waitLabel("That code is not right. 4 attempts left.")
	h.waitLabel("Attempts left: 4")
	if got := h.comp.telegram.State().Get().Step; got != telegramvm.StepCode {
		h.t.Fatalf("wizard left the code step after a wrong code: %q", got)
	}

	test.Type(h.onlyVisibleEntry(), testLoginCode)
	test.Tap(h.waitButton("Submit code"))
	test.Tap(h.waitButton("Continue"))
	h.waitRoute(model.RouteProjects)
}

func (h *harness) createProjectThroughUI(name string) {
	h.t.Helper()
	h.waitRoute(model.RouteProjects)
	h.waitLabel("No projects yet.")
	test.Tap(h.waitButton("New project"))
	test.Type(h.waitEntry("Project name"), name)
	test.Type(h.waitEntry("Password"), testProjectPassword)
	test.Type(h.waitEntry("Confirm password"), testProjectPassword)
	test.Tap(h.waitButton("Create project"))
	h.waitRoute(model.RouteDashboard)
}

func (h *harness) createBucketThroughUI(name string) {
	h.t.Helper()
	h.typeShortcut(fyne.Key2)
	h.waitRoute(model.RouteBuckets)
	test.Tap(h.waitButton("New bucket"))
	test.Type(h.waitOverlayEntry("my-bucket"), name)
	test.Tap(h.waitOverlayButton("Create bucket"))
	h.waitNoOverlay()
	h.waitFor("bucket listed", func() bool {
		for _, b := range h.session().models.buckets.State().Get().Buckets {
			if b.Name == name {
				return true
			}
		}
		return false
	})
}

func noisePNG(t *testing.T, side int) []byte {
	t.Helper()
	rng := rand.New(rand.NewSource(7))
	img := image.NewRGBA(image.Rect(0, 0, side, side))
	for y := 0; y < side; y++ {
		for x := 0; x < side; x++ {
			img.Set(x, y, color.RGBA{R: uint8(rng.Intn(256)), G: uint8(rng.Intn(256)), B: uint8(rng.Intn(256)), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

type transferLog struct {
	mu        sync.Mutex
	snapshots []objectsvm.TransferList
}

func (l *transferLog) record(list objectsvm.TransferList) {
	l.mu.Lock()
	l.snapshots = append(l.snapshots, list)
	l.mu.Unlock()
}

func (l *transferLog) all() []objectsvm.TransferList {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.snapshots)
}

func (h *harness) uploadFile(bucket, key string, data []byte) *transferLog {
	h.t.Helper()
	models := h.session().models
	path := filepath.Join(h.t.TempDir(), filepath.Base(key))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		h.t.Fatalf("write upload source: %v", err)
	}
	log := &transferLog{}
	cancel := models.objects.Transfers().Subscribe(log.record)
	defer cancel()
	spec, err := models.objects.PrepareUpload("", path)
	if err != nil {
		h.t.Fatalf("prepare upload: %v", err)
	}
	if spec.Key != key {
		h.t.Fatalf("upload key %q, want %q", spec.Key, key)
	}
	models.objects.Upload(h.session().ctx, bucket, []objectsvm.UploadSpec{spec}, objectsvm.Prompts{})
	h.pump()
	return log
}

func assertUploadProgress(t *testing.T, log *transferLog, size int64) {
	t.Helper()
	var sawSending bool
	var final objectsvm.Transfer
	for _, snapshot := range log.all() {
		for _, item := range snapshot.Items {
			if item.Kind != objectsvm.KindUpload {
				continue
			}
			if item.State == objectsvm.TransferSending {
				sawSending = true
			}
			final = item
		}
	}
	if !sawSending {
		t.Fatalf("no sending state was published for the upload")
	}
	if final.State != objectsvm.TransferDone || final.Sent != size || final.Total != size {
		t.Fatalf("final upload state %q sent %d of %d, want done %d", final.State, final.Sent, final.Total, size)
	}
}

func (h *harness) browseBucket(bucket string) {
	h.t.Helper()
	h.comp.sh.Navigator().Navigate(model.RouteObjects, model.Params{model.ParamBucket: bucket})
	h.waitFor("bucket opened", func() bool {
		st := h.session().models.objects.State().Get()
		return st.Bucket == bucket && !st.Loading
	})
}

func (h *harness) refreshObjects() []string {
	h.t.Helper()
	models := h.session().models
	models.objects.Refresh(h.session().ctx)
	h.pump()
	var keys []string
	for _, row := range models.objects.State().Get().Rows {
		if row.Kind == objectsvm.RowObject {
			keys = append(keys, row.Key)
		}
	}
	return keys
}

func (h *harness) downloadObject(bucket, key string, want []byte) {
	h.t.Helper()
	models := h.session().models
	target := filepath.Join(h.t.TempDir(), "downloaded.png")
	models.objects.Download(h.session().ctx, bucket, []objectsvm.DownloadRequest{{Key: key, TargetPath: target}}, objectsvm.Prompts{})
	h.pump()
	got, err := os.ReadFile(target)
	if err != nil {
		h.t.Fatalf("read downloaded file: %v", err)
	}
	if !bytes.Equal(got, want) {
		h.t.Fatalf("downloaded %d bytes differ from the %d uploaded", len(got), len(want))
	}
	if _, err := os.Stat(objectsvm.PartFilePath(target)); err == nil {
		h.t.Fatalf("part file left behind after download")
	}
	items := models.objects.Transfers().Get().Items
	last := items[len(items)-1]
	if last.Kind != objectsvm.KindDownload || last.State != objectsvm.TransferDone {
		h.t.Fatalf("download transfer %q %q, want a finished download", last.Kind, last.State)
	}
}

func (h *harness) showDetails(key string) {
	h.t.Helper()
	models := h.session().models
	models.objects.Focus(h.session().ctx, key)
	h.pump()
	st := models.objects.State().Get()
	if st.Details.Meta == nil || st.Details.Meta.Key != key {
		h.t.Fatalf("details for %q not loaded: %+v", key, st.Details)
	}
}

func (h *harness) deleteObject(bucket, key string) {
	h.t.Helper()
	models := h.session().models
	result := models.objects.Delete(h.session().ctx, bucket, []string{key})
	h.pump()
	if len(result.Failed) != 0 {
		h.t.Fatalf("delete failures: %+v", result.Failed)
	}
	if _, ok := h.admin.object(bucket, key); ok {
		h.t.Fatalf("object still on the server after delete")
	}
	if keys := h.refreshObjects(); slices.Contains(keys, key) {
		h.t.Fatalf("deleted object still listed: %v", keys)
	}
}

func TestAcceptanceLoginCreateProjectUploadDownload(t *testing.T) {
	h := newHarness(t)
	h.start()

	h.linkThroughUI()
	h.admin.mu.Lock()
	linked := h.admin.linked
	h.admin.mu.Unlock()
	if !linked {
		t.Fatalf("telegram not linked after the wizard")
	}
	if got := h.comp.telegram.State().Get().Step; got != telegramvm.StepDone {
		t.Fatalf("wizard step %q, want done", got)
	}
	h.createProjectThroughUI("photos")
	current, open := h.engine.Current()
	if !open || current.Name != "photos" {
		t.Fatalf("open project %+v %v, want photos", current, open)
	}
	if got := h.win.Title(); got != "butterDB Admin — photos" {
		t.Fatalf("window title = %q", got)
	}

	h.createBucketThroughUI("photos")
	if !slices.Equal(h.admin.bucketNames(), []string{"photos"}) {
		t.Fatalf("server buckets %v", h.admin.bucketNames())
	}
	data := noisePNG(t, 256)
	h.browseBucket("photos")
	log := h.uploadFile("photos", "holiday.png", data)
	assertUploadProgress(t, log, int64(len(data)))
	if keys := h.refreshObjects(); !slices.Equal(keys, []string{"holiday.png"}) {
		t.Fatalf("listed objects %v", keys)
	}
	if stored, ok := h.admin.object("photos", "holiday.png"); !ok || !bytes.Equal(stored.data, data) {
		t.Fatalf("server did not store the uploaded bytes")
	}
	h.downloadObject("photos", "holiday.png", data)
	h.deleteObject("photos", "holiday.png")
}

func TestBucketCRUDUsesTheAdminClient(t *testing.T) {
	h := newHarness(t, withLinked())
	h.startInProject("photos")

	h.createBucketThroughUI("photos")
	if h.admin.callCount("POST", adminv1.PathBuckets) != 1 {
		t.Fatalf("create calls: buckets %d", h.admin.callCount("POST", adminv1.PathBuckets))
	}
	models := h.session().models
	ctx := h.session().ctx
	if !models.buckets.Update(ctx, "photos", bucketSettings(true)) {
		t.Fatalf("bucket update failed: %+v", models.buckets.State().Get().Problem)
	}
	if got := h.admin.bucketPolicy("photos"); got != "public-read" {
		t.Fatalf("server policy after update = %q", got)
	}
	if !models.buckets.Delete(ctx, "photos") {
		t.Fatalf("bucket delete failed: %+v", models.buckets.State().Get().Problem)
	}
	h.pump()
	if names := h.admin.bucketNames(); len(names) != 0 {
		t.Fatalf("server still has buckets %v", names)
	}
}

func TestObjectUploadDownloadAndDetailsWork(t *testing.T) {
	h := newHarness(t, withLinked())
	h.admin.seedBucket("photos")
	h.startInProject("photos")

	data := noisePNG(t, 256)
	h.browseBucket("photos")
	log := h.uploadFile("photos", "holiday.png", data)
	assertUploadProgress(t, log, int64(len(data)))
	if keys := h.refreshObjects(); !slices.Equal(keys, []string{"holiday.png"}) {
		t.Fatalf("listed objects %v", keys)
	}
	h.showDetails("holiday.png")
	h.downloadObject("photos", "holiday.png", data)
	h.deleteObject("photos", "holiday.png")
}

func TestUnauthenticatedCallRoutesToTelegram(t *testing.T) {
	h := newHarness(t, withLinked())
	h.startInProject("photos")
	session := h.session()

	h.admin.setSignedOut(true)
	h.typeShortcut(fyne.Key2)

	h.waitRoute(model.RouteTelegram)
	if session.ctx.Err() == nil {
		t.Fatalf("session context still live after Telegram signed the app out")
	}
	if h.comp.currentSession() != nil {
		t.Fatalf("session still registered after sign out")
	}
	h.waitFor("project closed", func() bool { return !h.engine.isOpen() })
}

func bucketSettings(publicRead bool) bucketsvm.Settings {
	return bucketsvm.Settings{PublicRead: publicRead, CacheMaxAgeSeconds: bucketsvm.DefaultCacheMaxAgeSeconds}
}
