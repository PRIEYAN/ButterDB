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
	"strings"
	"sync"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"github.com/zalando/go-keyring"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	bucketsvm "github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/buckets/vm"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/connect/credstore"
	connectvm "github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/connect/vm"
	objectsvm "github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/objects/vm"
	telegramvm "github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/telegramlink/vm"
)

func (h *harness) claimThroughUI(admin *fakeAdmin) {
	h.t.Helper()
	h.waitRoute(model.RouteConnect)
	test.Type(h.waitEntry("host or host:port"), admin.host())
	test.Tap(h.waitButton("Continue"))
	test.Tap(h.waitButton("Fingerprints match"))
	test.Type(h.waitEntry("bdst1_…"), testSetupToken)
	test.Tap(h.waitButton("Claim"))
}

func (h *harness) linkThroughUI(t *testing.T, admin *fakeAdmin) {
	h.t.Helper()
	h.waitRoute(model.RouteTelegram)
	test.Type(h.waitEntry("digits only"), "123456")
	test.Type(h.waitEntry("32 hexadecimal characters"), testAPIHash)
	test.Tap(h.waitButton("Save app credentials"))
	test.Type(h.waitEntry("+447700900134"), "+447700900134")
	test.Tap(h.waitButton("Send code"))

	h.waitButton("Submit code")
	h.waitFor("code step entry", func() bool { return h.session().models.telegram.State().Get().Step == telegramvm.StepCode })
	code := h.onlyVisibleEntry()
	test.Type(code, "00000")
	test.Tap(h.waitButton("Submit code"))
	h.waitFor("wrong code answered", func() bool { return admin.callCount("POST", adminv1.PathSetupTelegramCode) == 1 })
	h.waitLabel("That code is not right. 4 attempts left.")
	h.waitLabel("Attempts left: 4")
	if got := h.session().models.telegram.State().Get().Step; got != telegramvm.StepCode {
		h.t.Fatalf("wizard left the code step after a wrong code: %q", got)
	}

	test.Type(h.onlyVisibleEntry(), testLoginCode)
	test.Tap(h.waitButton("Submit code"))
	test.Tap(h.waitButton("Continue to dashboard"))
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

func (h *harness) createKeyThroughUI(name string) {
	h.t.Helper()
	h.typeShortcut(fyne.Key3)
	h.waitRoute(model.RouteKeys)
	test.Tap(h.waitButton("New key"))
	test.Type(h.waitOverlayEntry("website prod"), name)
	test.Tap(h.waitOverlayCheck("All buckets, including future ones"))
	test.Tap(h.waitOverlayButton("Create key"))

	ack := h.waitOverlayCheck("I have stored the secret")
	done := h.waitOverlayButton("Done").(fyne.Disableable)
	if !done.Disabled() {
		h.t.Fatalf("Done is enabled before the secret is acknowledged")
	}
	if h.session().models.keys.State().Get().Secret == nil {
		h.t.Fatalf("secret missing while its dialog is open")
	}
	test.Tap(ack)
	if done.Disabled() {
		h.t.Fatalf("Done is still disabled after acknowledging")
	}
	test.Tap(done.(fyne.Tappable))
	h.waitNoOverlay()
	h.waitFor("secret dropped", func() bool { return h.session().models.keys.State().Get().Secret == nil })
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

func (h *harness) previewObject(key string, want []byte) {
	h.t.Helper()
	models := h.session().models
	models.objects.Focus(h.session().ctx, key)
	h.pump()
	st := models.objects.State().Get()
	if st.Details.Meta == nil || st.Details.Meta.Key != key {
		h.t.Fatalf("details for %q not loaded: %+v", key, st.Details)
	}
	if st.Preview.Status != objectsvm.PreviewReady || !bytes.Equal(st.Preview.Data, want) {
		h.t.Fatalf("preview status %q with %d bytes, want ready with %d", st.Preview.Status, len(st.Preview.Data), len(want))
	}
	h.settle()
	if got := models.objects.State().Get().Preview.Status; got != objectsvm.PreviewReady {
		h.t.Fatalf("preview did not survive decoding in the view, status %q", got)
	}
}

func (h *harness) presignObject(admin *fakeAdmin, bucket, key string) {
	h.t.Helper()
	models := h.session().models
	signers, problem := models.objects.SigningKeys(h.session().ctx, bucket)
	if problem != nil || len(signers) == 0 {
		h.t.Fatalf("no signing key offered for %q: %+v", bucket, problem)
	}
	outcome := models.objects.Presign(h.session().ctx, bucket, key, signers[0].ID, 3600)
	if outcome.Problem != nil {
		h.t.Fatalf("presign problem: %+v", outcome.Problem)
	}
	if !strings.Contains(outcome.URL.URL, "/"+bucket+"/"+key) {
		h.t.Fatalf("presigned URL %q does not name the object", outcome.URL.URL)
	}
	admin.mu.Lock()
	sent := admin.lastPresign
	admin.mu.Unlock()
	if sent.Key != key || sent.AccessKeyID != signers[0].ID || sent.ExpiresInSeconds != 3600 {
		h.t.Fatalf("presign request %+v does not match the choice", sent)
	}
}

func (h *harness) deleteObject(admin *fakeAdmin, bucket, key string) {
	h.t.Helper()
	models := h.session().models
	result := models.objects.Delete(h.session().ctx, bucket, []string{key})
	h.pump()
	if len(result.Failed) != 0 {
		h.t.Fatalf("delete failures: %+v", result.Failed)
	}
	if _, ok := admin.object(bucket, key); ok {
		h.t.Fatalf("object still on the server after delete")
	}
	if keys := h.refreshObjects(); slices.Contains(keys, key) {
		h.t.Fatalf("deleted object still listed: %v", keys)
	}
}

func TestAcceptanceClaimLinkCreateUploadDownload(t *testing.T) {
	admin := newFakeAdmin(t, fixedNow)
	h := newHarness(t)
	h.start()

	h.waitRoute(model.RouteConnect)
	test.Type(h.waitEntry("host or host:port"), admin.host())
	test.Tap(h.waitButton("Continue"))
	h.waitLabel("Verify this server")
	h.waitFor("probed fingerprint on screen", func() bool {
		return h.hasLabel(connectvm.FingerprintLines(strings.TrimPrefix(admin.pin, "SHA256:")))
	})
	if calls := admin.totalCalls(); calls != 0 {
		t.Fatalf("probe sent %d http requests, want a handshake only", calls)
	}
	test.Tap(h.waitButton("Fingerprints match"))
	test.Type(h.waitEntry("bdst1_…"), testSetupToken)
	test.Tap(h.waitButton("Claim"))
	h.linkThroughUI(t, admin)

	profile, err := h.store.Get(testServerID)
	if err != nil {
		t.Fatalf("profile after claim: %v", err)
	}
	if profile.Trust != adminv1Pinned(admin.pin) || profile.BaseURL != admin.srv.URL {
		t.Fatalf("stored trust %+v url %q", profile.Trust, profile.BaseURL)
	}

	h.createBucketThroughUI("photos")
	if !slices.Equal(admin.bucketNames(), []string{"photos"}) {
		t.Fatalf("server buckets %v", admin.bucketNames())
	}
	h.createKeyThroughUI("deploy")
	if len(admin.keyIDs()) != 1 {
		t.Fatalf("server keys %v", admin.keyIDs())
	}

	data := noisePNG(t, 256)
	h.browseBucket("photos")
	log := h.uploadFile("photos", "holiday.png", data)
	assertUploadProgress(t, log, int64(len(data)))
	if keys := h.refreshObjects(); !slices.Equal(keys, []string{"holiday.png"}) {
		t.Fatalf("listed objects %v", keys)
	}
	if stored, ok := admin.object("photos", "holiday.png"); !ok || !bytes.Equal(stored.data, data) {
		t.Fatalf("server did not store the uploaded bytes")
	}
	h.downloadObject("photos", "holiday.png", data)
	h.presignObject(admin, "photos", "holiday.png")
	h.deleteObject(admin, "photos", "holiday.png")
}

func TestSetupAndTelegramLinkWizardCompletes(t *testing.T) {
	admin := newFakeAdmin(t, fixedNow)
	h := newHarness(t)
	h.start()

	h.claimThroughUI(admin)
	h.linkThroughUI(t, admin)

	admin.mu.Lock()
	linked, claimed := admin.linked, admin.claimed
	admin.mu.Unlock()
	if !linked || !claimed {
		t.Fatalf("server claimed %v linked %v after the wizard", claimed, linked)
	}
	if got := h.session().models.telegram.State().Get().Step; got != telegramvm.StepDone {
		t.Fatalf("wizard step %q, want done", got)
	}
	nav := h.comp.sh.Navigator()
	nav.Navigate(model.RouteBuckets, nil)
	h.settle()
	if got := h.route(); got != model.RouteBuckets {
		t.Fatalf("data routes stayed disabled after linking, route %q", got)
	}
	nav.Navigate(model.RouteTelegram, nil)
	h.settle()
	if got := h.route(); got != model.RouteBuckets {
		t.Fatalf("telegram route still reachable after linking, route %q", got)
	}
}

func TestBucketAndKeyCRUDUseTheAdminClient(t *testing.T) {
	admin := newFakeAdmin(t, fixedNow, withLinked())
	h := newHarness(t)
	h.seedProfile(admin, model.TokenKeychain)
	h.start()
	h.waitRoute(model.RouteDashboard)

	h.createBucketThroughUI("photos")
	h.createKeyThroughUI("deploy")
	if admin.callCount("POST", adminv1.PathBuckets) != 1 || admin.callCount("POST", adminv1.PathAccessKeys) != 1 {
		t.Fatalf("create calls: buckets %d keys %d", admin.callCount("POST", adminv1.PathBuckets), admin.callCount("POST", adminv1.PathAccessKeys))
	}

	models := h.session().models
	ctx := h.session().ctx
	keys := admin.keyIDs()
	if !models.buckets.Update(ctx, "photos", bucketSettings(true)) {
		t.Fatalf("bucket update failed: %+v", models.buckets.State().Get().Problem)
	}
	if got := admin.bucketPolicy("photos"); got != "public-read" {
		t.Fatalf("server policy after update = %q", got)
	}
	if !models.keys.Revoke(ctx, keys[0]) {
		t.Fatalf("revoke failed: %+v", models.keys.State().Get().Problem)
	}
	if got := admin.keyStatus(keys[0]); got != "revoked" {
		t.Fatalf("server key status after revoke = %q", got)
	}
	if !models.buckets.Delete(ctx, "photos") {
		t.Fatalf("bucket delete failed: %+v", models.buckets.State().Get().Problem)
	}
	h.pump()
	if names := admin.bucketNames(); len(names) != 0 {
		t.Fatalf("server still has buckets %v", names)
	}
	if admin.callsWithPrefix("PATCH", adminv1.PathBuckets+"/") != 1 || admin.callsWithPrefix("DELETE", adminv1.PathBuckets+"/") != 1 || admin.callsWithPrefix("DELETE", adminv1.PathAccessKeys+"/") != 1 {
		t.Fatalf("update, delete and revoke each need one admin call")
	}
}

func TestObjectUploadDownloadAndPreviewWork(t *testing.T) {
	admin := newFakeAdmin(t, fixedNow, withLinked())
	admin.seedBucket("photos")
	admin.seedKey("reader")
	h := newHarness(t)
	h.seedProfile(admin, model.TokenKeychain)
	h.start()
	h.waitRoute(model.RouteDashboard)

	data := noisePNG(t, 256)
	h.browseBucket("photos")
	log := h.uploadFile("photos", "holiday.png", data)
	assertUploadProgress(t, log, int64(len(data)))
	if keys := h.refreshObjects(); !slices.Equal(keys, []string{"holiday.png"}) {
		t.Fatalf("listed objects %v", keys)
	}
	h.previewObject("holiday.png", data)
	h.downloadObject("photos", "holiday.png", data)
	h.presignObject(admin, "photos", "holiday.png")
	h.deleteObject(admin, "photos", "holiday.png")
}

func TestDeviceTokenIsStoredInOSKeychain(t *testing.T) {
	admin := newFakeAdmin(t, fixedNow)
	h := newHarness(t)
	h.start()

	h.claimThroughUI(admin)
	h.waitRoute(model.RouteTelegram)

	stored, err := keyring.Get(credstore.KeychainService, testServerID)
	if err != nil || stored != testDeviceTok {
		t.Fatalf("keychain entry for %s: %q, %v", testServerID, stored, err)
	}
	profile, err := h.store.Get(testServerID)
	if err != nil {
		t.Fatalf("profile: %v", err)
	}
	if profile.TokenStorage != model.TokenKeychain {
		t.Fatalf("profile token storage %q, want keychain", profile.TokenStorage)
	}
	if !admin.tokenWasPresented(testDeviceTok) {
		t.Fatalf("the session never used the keychain token")
	}
	err = filepath.WalkDir(h.configDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if bytes.Contains(content, []byte(testDeviceTok)) || bytes.Contains(content, []byte(testSetupToken)) {
			t.Errorf("a token reached disk in %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan config dir: %v", err)
	}
	if token, err := h.opts.Files.Get(testServerID); err == nil {
		t.Fatalf("token also stored in a file: %q", token)
	}
}

func TestAcceptancePinMismatch(t *testing.T) {
	admin := newFakeAdmin(t, fixedNow, withLinked())
	h := newHarness(t)
	profile := h.seedProfile(admin, model.TokenKeychain)
	profile.Trust = adminv1Pinned("SHA256:" + strings.Repeat("ab", 32))
	if err := h.store.Put(profile); err != nil {
		t.Fatalf("pin the wrong key: %v", err)
	}
	h.start()

	h.waitFor("pin mismatch step", func() bool { return h.comp.connectVM.State().Get().Step == connectvm.StepPinMismatch })
	h.waitLabel("Server identity changed")
	st := h.comp.connectVM.State().Get()
	if st.Mismatch == nil || st.Mismatch.Received != connectvm.FingerprintLines(admin.pin) {
		t.Fatalf("mismatch view %+v does not show the presented key", st.Mismatch)
	}
	if got := h.route(); got != model.RouteConnect {
		t.Fatalf("route %q, want connect", got)
	}
	if h.comp.currentSession() != nil {
		t.Fatalf("a session was built against a server with a changed key")
	}
	if calls := admin.totalCalls(); calls != 0 {
		t.Fatalf("%d http requests reached the server before the pin check", calls)
	}
	if token, err := keyring.Get(credstore.KeychainService, testServerID); err != nil || token != testDeviceTok {
		t.Fatalf("stored token was lost on a pin mismatch: %q, %v", token, err)
	}
	h.comp.sh.Navigator().Navigate(model.RouteDashboard, nil)
	h.settle()
	if got := h.route(); got != model.RouteConnect {
		t.Fatalf("other screens reachable during a pin mismatch, route %q", got)
	}
	test.Tap(h.waitButton("Disconnect"))
	h.waitFor("back to profiles", func() bool { return h.comp.connectVM.State().Get().Step == connectvm.StepProfiles })
}

func TestAcceptanceDeviceRevoked(t *testing.T) {
	admin := newFakeAdmin(t, fixedNow, withLinked())
	h := newHarness(t)
	h.seedProfile(admin, model.TokenKeychain)
	h.start()
	h.waitRoute(model.RouteDashboard)
	session := h.session()

	admin.setRevoked(true)
	h.typeShortcut(fyne.Key2)

	h.waitRoute(model.RouteConnect)
	h.waitFor("pairing form", func() bool { return h.comp.connectVM.State().Get().Step == connectvm.StepPair })
	if _, err := keyring.Get(credstore.KeychainService, testServerID); err == nil {
		t.Fatalf("revoked device token still in the keychain")
	}
	if notice := h.comp.connectVM.State().Get().Notice; !strings.Contains(notice, "revoked") {
		t.Fatalf("notice %q does not say the device was revoked", notice)
	}
	if session.ctx.Err() == nil {
		t.Fatalf("session context still live after the device was revoked")
	}
	if h.comp.currentSession() != nil {
		t.Fatalf("session still registered after revocation")
	}
	h.waitButton("Pair")
}

func TestUnauthenticatedCallRoutesToPairing(t *testing.T) {
	admin := newFakeAdmin(t, fixedNow, withLinked())
	h := newHarness(t)
	h.seedProfile(admin, model.TokenKeychain)
	h.start()
	h.waitRoute(model.RouteDashboard)

	h.session().api.SetToken("bdt1_wrong")
	h.typeShortcut(fyne.Key3)

	h.waitRoute(model.RouteConnect)
	h.waitFor("pairing form", func() bool { return h.comp.connectVM.State().Get().Step == connectvm.StepPair })
	if _, err := keyring.Get(credstore.KeychainService, testServerID); err == nil {
		t.Fatalf("token kept after the server refused it")
	}
	if notice := h.comp.connectVM.State().Get().Notice; !strings.Contains(notice, "no longer signed in") {
		t.Fatalf("notice %q", notice)
	}
}

func bucketSettings(publicRead bool) bucketsvm.Settings {
	return bucketsvm.Settings{PublicRead: publicRead, CacheMaxAgeSeconds: bucketsvm.DefaultCacheMaxAgeSeconds}
}
