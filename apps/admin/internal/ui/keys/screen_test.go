package keys

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/keys/vm"
)

var testNow = time.Date(2026, 11, 16, 9, 30, 0, 0, time.UTC)

const (
	testKeyID  = "BDBK7Q2M9X4T1W8Z3R6Y"
	testSecret = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
)

type fakeClipboard struct{ text string }

func (c *fakeClipboard) SetText(text string) { c.text = text }
func (c *fakeClipboard) Text() string        { return c.text }

type fakeNav struct {
	routes []model.Route
}

func (n *fakeNav) Navigate(r model.Route, _ model.Params) { n.routes = append(n.routes, r) }
func (n *fakeNav) Current() model.Route                   { return "" }
func (n *fakeNav) SetEnabled(model.Route, bool, string)   {}

type fakeClient struct {
	keys       []adminv1.AccessKey
	buckets    []string
	createErr  error
	createReqs []adminv1.CreateAccessKeyRequest
	updateReqs []adminv1.UpdateAccessKeyRequest
	revoked    []string
}

func (f *fakeClient) ListAccessKeys(_ context.Context, includeRevoked bool) (adminv1.AccessKeyList, error) {
	var out []adminv1.AccessKey
	for _, k := range f.keys {
		if includeRevoked || k.Status != "revoked" {
			out = append(out, k)
		}
	}
	return adminv1.AccessKeyList{AccessKeys: out}, nil
}

func (f *fakeClient) CreateAccessKey(_ context.Context, req adminv1.CreateAccessKeyRequest) (adminv1.AccessKeyCreated, error) {
	f.createReqs = append(f.createReqs, req)
	if f.createErr != nil {
		return adminv1.AccessKeyCreated{}, f.createErr
	}
	key := adminv1.AccessKey{AccessKeyID: testKeyID, Name: req.Name, Status: "active", Scope: req.Scope}
	f.keys = append(f.keys, key)
	return adminv1.AccessKeyCreated{AccessKey: key, SecretAccessKey: testSecret, S3Endpoint: "https://s3.example.com", S3Region: "us-east-1"}, nil
}

func (f *fakeClient) UpdateAccessKey(_ context.Context, id string, req adminv1.UpdateAccessKeyRequest) (adminv1.AccessKey, error) {
	f.updateReqs = append(f.updateReqs, req)
	return adminv1.AccessKey{AccessKeyID: id}, nil
}

func (f *fakeClient) RevokeAccessKey(_ context.Context, id string) (adminv1.AccessKey, error) {
	f.revoked = append(f.revoked, id)
	for i := range f.keys {
		if f.keys[i].AccessKeyID == id {
			f.keys[i].Status = "revoked"
		}
	}
	return adminv1.AccessKey{AccessKeyID: id, Status: "revoked"}, nil
}

func (f *fakeClient) ListBuckets(context.Context) (adminv1.BucketList, error) {
	var list adminv1.BucketList
	for _, name := range f.buckets {
		list.Buckets = append(list.Buckets, adminv1.Bucket{Name: name})
	}
	return list, nil
}

func (f *fakeClient) Status(context.Context) (adminv1.Status, error) { return adminv1.Status{}, nil }

type fixture struct {
	client    *fakeClient
	vm        *vm.ViewModel
	nav       *fakeNav
	clip      *fakeClipboard
	screen    *Screen
	win       fyne.Window
	scheduled []func()
	delays    []time.Duration
}

func newFixture(t *testing.T, client *fakeClient) *fixture {
	t.Helper()
	test.NewTempApp(t)
	f := &fixture{client: client, nav: &fakeNav{}, clip: &fakeClipboard{}}
	f.vm = vm.New(client, func() time.Time { return testNow })
	f.screen = NewScreenWithEnv(f.vm, f.nav, Env{
		Window:    func() fyne.Window { return f.win },
		Clipboard: f.clip,
		Spawn:     func(fn func()) { fn() },
		Schedule: func(after time.Duration, run func()) func() {
			f.scheduled = append(f.scheduled, run)
			f.delays = append(f.delays, after)
			return func() {}
		},
	})
	f.win = test.NewTempWindow(t, f.screen.Content())
	f.win.Resize(fyne.NewSize(1300, 700))
	return f
}

func (f *fixture) show() { f.screen.Show(context.Background(), nil) }

func (f *fixture) overlay() fyne.CanvasObject { return f.win.Canvas().Overlays().Top() }

func (f *fixture) selectRow(row int) {
	f.screen.list.table.Select(widget.TableCellID{Row: row, Col: 0})
}

func (f *fixture) cell(row, col int) string {
	label := widget.NewLabel("")
	f.screen.list.updateCell(widget.TableCellID{Row: row, Col: col}, label)
	return label.Text
}

func (f *fixture) typeInFocused(text string) {
	focused, ok := f.win.Canvas().Focused().(fyne.Focusable)
	if !ok || focused == nil {
		panic("nothing focused")
	}
	test.Type(focused, text)
}

func descend(root fyne.CanvasObject) []fyne.CanvasObject {
	if root == nil {
		return nil
	}
	return test.LaidOutObjects(root)
}

func asButton(o fyne.CanvasObject) *widget.Button {
	if b, ok := o.(*widget.Button); ok {
		return b
	}
	v := reflect.ValueOf(o)
	if v.Kind() != reflect.Pointer || v.Elem().Kind() != reflect.Struct {
		return nil
	}
	embedded := v.Elem().FieldByName("Button")
	if !embedded.IsValid() || embedded.Type() != reflect.TypeOf(widget.Button{}) {
		return nil
	}
	return embedded.Addr().Interface().(*widget.Button)
}

func buttonNamed(root fyne.CanvasObject, text string) *widget.Button {
	for _, o := range descend(root) {
		if b := asButton(o); b != nil && b.Text == text {
			return b
		}
	}
	return nil
}

func tapNamed(t *testing.T, root fyne.CanvasObject, text string) {
	t.Helper()
	b := buttonNamed(root, text)
	if b == nil {
		t.Fatalf("no button %q", text)
	}
	test.Tap(b)
}

func checkNamed(root fyne.CanvasObject, text string) *widget.Check {
	for _, o := range descend(root) {
		if c, ok := o.(*widget.Check); ok && c.Text == text {
			return c
		}
	}
	return nil
}

func checkGroups(root fyne.CanvasObject) []*widget.CheckGroup {
	var out []*widget.CheckGroup
	for _, o := range descend(root) {
		if g, ok := o.(*widget.CheckGroup); ok {
			out = append(out, g)
		}
	}
	return out
}

func hasLabelContaining(root fyne.CanvasObject, text string) bool {
	for _, o := range descend(root) {
		if l, ok := o.(*widget.Label); ok && strings.Contains(l.Text, text) {
			return true
		}
	}
	return false
}

func sampleKeys() *fakeClient {
	used := testNow.Add(-3 * time.Hour)
	return &fakeClient{
		buckets: []string{"photos", "avatars"},
		keys: []adminv1.AccessKey{
			{AccessKeyID: "BDBKAAAAAAAAAAAAAAAA", Name: "website", Status: "active", CreatedAt: testNow.Add(-48 * time.Hour), LastUsedAt: &used,
				Scope: adminv1.Scope{Buckets: []string{"photos"}, Actions: []string{"read", "list"}}},
			{AccessKeyID: "BDBKBBBBBBBBBBBBBBBB", Name: "backup", Status: "active",
				Scope: adminv1.Scope{Buckets: []string{"*"}, Actions: []string{"read", "list", "write", "delete"}}},
			{AccessKeyID: "BDBKCCCCCCCCCCCCCCCC", Name: "old", Status: "revoked",
				Scope: adminv1.Scope{Buckets: []string{"photos"}, Actions: []string{"read"}}},
		},
	}
}

func createKey(t *testing.T, f *fixture) {
	t.Helper()
	tapNamed(t, f.screen.Content(), "New key")
	f.typeInFocused("website prod")
	groups := checkGroups(f.overlay())
	groups[0].SetSelected([]string{"photos"})
	tapNamed(t, f.overlay(), "Create key")
}

func TestListRendersRows(t *testing.T) {
	f := newFixture(t, sampleKeys())
	f.show()
	want := [][]string{
		{"website", "BDBKAAAAAAAAAAAAAAAA", "photos", "Read, List", "active", "3 h ago", "2026-11-14 09:30"},
		{"backup", "BDBKBBBBBBBBBBBBBBBB", "All buckets", "Read, List, Write, Delete", "active", "never", ""},
	}
	for row, cells := range want {
		for col, text := range cells {
			if got := f.cell(row, col); got != text {
				t.Errorf("cell(%d,%d) = %q, want %q", row, col, got, text)
			}
		}
	}
	if got := len(f.screen.list.keys); got != 2 {
		t.Fatalf("rows = %d, revoked keys must be hidden by default", got)
	}
}

func TestShowRevokedToggleReloads(t *testing.T) {
	f := newFixture(t, sampleKeys())
	f.show()
	revoked := f.screen.toolbar.revoked
	test.Tap(revoked)
	if got := len(f.screen.list.keys); got != 3 {
		t.Fatalf("rows = %d after Show revoked", got)
	}
	if got := f.cell(2, 4); got != "revoked" {
		t.Fatalf("status = %q", got)
	}
	f.selectRow(2)
	for _, name := range []string{"Edit…", "Revoke…"} {
		if buttonNamed(f.screen.Content(), name).Disabled() == false {
			t.Errorf("%q enabled for a revoked key", name)
		}
	}
	if buttonNamed(f.screen.Content(), "Copy key ID").Disabled() {
		t.Errorf("copy disabled for a revoked key")
	}
	test.Tap(revoked)
	if got := len(f.screen.list.keys); got != 2 {
		t.Fatalf("rows = %d after hiding revoked", got)
	}
}

func TestCreateFlowSendsScopeAndShowsSecret(t *testing.T) {
	f := newFixture(t, sampleKeys())
	f.show()
	createKey(t, f)
	if len(f.client.createReqs) != 1 {
		t.Fatalf("create requests = %d", len(f.client.createReqs))
	}
	req := f.client.createReqs[0]
	if req.Name != "website prod" || !reflect.DeepEqual(req.Scope.Buckets, []string{"photos"}) || !reflect.DeepEqual(req.Scope.Actions, []string{"read", "list"}) {
		t.Fatalf("request = %+v", req)
	}
	if f.overlay() == nil || !hasLabelContaining(f.overlay(), "Save this secret now") {
		t.Fatalf("secret dialog not shown")
	}
	if !hasLabelContaining(f.overlay(), testSecret) || !hasLabelContaining(f.overlay(), testKeyID) {
		t.Fatalf("dialog does not show the key")
	}
}

func TestCreateAllBucketsSendsWildcard(t *testing.T) {
	f := newFixture(t, sampleKeys())
	f.show()
	tapNamed(t, f.screen.Content(), "New key")
	f.typeInFocused("everything")
	test.Tap(checkNamed(f.overlay(), allBucketsLabel))
	tapNamed(t, f.overlay(), "Read and write")
	tapNamed(t, f.overlay(), "Create key")
	got := f.client.createReqs[0].Scope
	if !reflect.DeepEqual(got.Buckets, []string{"*"}) || len(got.Actions) != 4 {
		t.Fatalf("scope = %+v", got)
	}
}

func TestAllBucketsDisablesBucketList(t *testing.T) {
	f := newFixture(t, sampleKeys())
	f.show()
	tapNamed(t, f.screen.Content(), "New key")
	groups := checkGroups(f.overlay())
	if groups[0].Disabled() {
		t.Fatalf("bucket list disabled before ticking all buckets")
	}
	test.Tap(checkNamed(f.overlay(), allBucketsLabel))
	if !groups[0].Disabled() {
		t.Fatalf("bucket list enabled while all buckets is ticked")
	}
	if !reflect.DeepEqual(groups[0].Options, []string{"avatars", "photos"}) {
		t.Fatalf("bucket options = %v", groups[0].Options)
	}
}

func TestCreateWithoutScopeShowsInlineError(t *testing.T) {
	f := newFixture(t, sampleKeys())
	f.show()
	tapNamed(t, f.screen.Content(), "New key")
	f.typeInFocused("nothing")
	tapNamed(t, f.overlay(), "Create key")
	if len(f.client.createReqs) != 0 {
		t.Fatalf("request sent without a bucket")
	}
	if !hasLabelContaining(f.overlay(), "Choose at least one bucket and one permission.") {
		t.Fatalf("scope error not shown")
	}
	if f.overlay() == nil || buttonNamed(f.overlay(), "Create key") == nil {
		t.Fatalf("form closed after an inline error")
	}
}

func TestCreateServerErrorKeepsFormOpen(t *testing.T) {
	client := sampleKeys()
	client.createErr = &adminv1.APIError{Status: http.StatusInternalServerError, Body: adminv1.ErrorBody{Code: adminv1.CodeInternal, RequestID: "req-9", Message: "boom"}}
	f := newFixture(t, client)
	f.show()
	createKey(t, f)
	if f.overlay() == nil || buttonNamed(f.overlay(), "Create key") == nil {
		t.Fatalf("form closed after an error")
	}
	if !hasLabelContaining(f.overlay(), "request req-9") {
		t.Fatalf("error text missing")
	}
	if hasLabelContaining(f.overlay(), "boom") {
		t.Fatalf("raw server message shown")
	}
	if f.vm.State().Get().Secret != nil {
		t.Fatalf("secret present after failure")
	}
}

func TestSecretDialogNotDismissableUntilAcknowledged(t *testing.T) {
	f := newFixture(t, sampleKeys())
	f.show()
	createKey(t, f)
	secretOverlay := f.overlay()
	done := buttonNamed(secretOverlay, "Done")
	if done == nil || !done.Disabled() {
		t.Fatalf("Done missing or enabled before acknowledging")
	}
	canvas := f.win.Canvas()
	canvas.Focused().TypedKey(&fyne.KeyEvent{Name: fyne.KeyEscape})
	if f.overlay() != secretOverlay {
		t.Fatalf("Escape dismissed the secret dialog")
	}
	test.TapCanvas(canvas, fyne.NewPos(3, 3))
	if f.overlay() != secretOverlay {
		t.Fatalf("a click outside dismissed the secret dialog")
	}
	test.Tap(done)
	if f.overlay() != secretOverlay || f.vm.State().Get().Secret == nil {
		t.Fatalf("disabled Done dismissed the dialog")
	}
	ack := checkNamed(secretOverlay, acknowledgeBox)
	test.Tap(ack)
	if done.Disabled() {
		t.Fatalf("Done still disabled after acknowledging")
	}
	test.Tap(ack)
	if !done.Disabled() {
		t.Fatalf("Done stayed enabled after un-ticking")
	}
	test.Tap(ack)
	test.Tap(done)
	if f.overlay() != nil {
		t.Fatalf("dialog still open after Done")
	}
	if f.vm.State().Get().Secret != nil {
		t.Fatalf("view model kept the secret after Done")
	}
}

func TestCopyButtonsUseClipboard(t *testing.T) {
	f := newFixture(t, sampleKeys())
	f.show()
	createKey(t, f)
	overlay := f.overlay()
	var copies []*widget.Button
	for _, o := range descend(overlay) {
		if b := asButton(o); b != nil && b.Text == "Copy" {
			copies = append(copies, b)
		}
	}
	want := []string{testKeyID, testSecret, "https://s3.example.com", "us-east-1"}
	if len(copies) != len(want) {
		t.Fatalf("copy buttons = %d, want %d", len(copies), len(want))
	}
	for i, b := range copies {
		test.Tap(b)
		if f.clip.text != want[i] {
			t.Errorf("copy %d put %q on the clipboard, want %q", i, f.clip.text, want[i])
		}
	}
	tapNamed(t, overlay, copyEnvLabel)
	wantEnv := "AWS_ACCESS_KEY_ID=" + testKeyID + "\nAWS_SECRET_ACCESS_KEY=" + testSecret +
		"\nAWS_ENDPOINT_URL_S3=https://s3.example.com\nAWS_REGION=us-east-1"
	if f.clip.text != wantEnv {
		t.Fatalf("env block = %q", f.clip.text)
	}
}

func TestCopiedSecretClearedAfterSixtySeconds(t *testing.T) {
	f := newFixture(t, sampleKeys())
	f.show()
	createKey(t, f)
	tapNamed(t, f.overlay(), copyEnvLabel)
	if len(f.scheduled) == 0 || f.delays[len(f.delays)-1] != 60*time.Second {
		t.Fatalf("no 60 s clear scheduled: %v", f.delays)
	}
	f.scheduled[len(f.scheduled)-1]()
	if f.clip.text != "" {
		t.Fatalf("clipboard still holds the secret: %q", f.clip.text)
	}
}

func TestCopyKeyIDButtonAndShortcut(t *testing.T) {
	f := newFixture(t, sampleKeys())
	f.show()
	f.selectRow(1)
	tapNamed(t, f.screen.Content(), "Copy key ID")
	if f.clip.text != "BDBKBBBBBBBBBBBBBBBB" {
		t.Fatalf("clipboard = %q", f.clip.text)
	}
	f.clip.text = ""
	f.screen.list.table.TypedShortcut(&fyne.ShortcutCopy{})
	if f.clip.text != "BDBKBBBBBBBBBBBBBBBB" {
		t.Fatalf("Ctrl+C did not copy the focused key: %q", f.clip.text)
	}
	if len(f.scheduled) != 0 {
		t.Fatalf("key ID copy was scheduled for clearing")
	}
}

func TestRevokeAsksConfirmationThenRefreshes(t *testing.T) {
	f := newFixture(t, sampleKeys())
	f.show()
	f.selectRow(0)
	tapNamed(t, f.screen.Content(), "Revoke…")
	overlay := f.overlay()
	if !hasLabelContaining(overlay, "Applications using this key stop working immediately, and presigned URLs signed with it stop working.") {
		t.Fatalf("warning missing")
	}
	if len(f.client.revoked) != 0 {
		t.Fatalf("revoked before confirming")
	}
	tapNamed(t, overlay, "Revoke key")
	if len(f.client.revoked) != 1 || f.client.revoked[0] != "BDBKAAAAAAAAAAAAAAAA" {
		t.Fatalf("revoked = %v", f.client.revoked)
	}
	if got := len(f.screen.list.keys); got != 1 {
		t.Fatalf("rows = %d after revoke", got)
	}
	if f.screen.list.selected != "" {
		t.Fatalf("selection kept for a vanished row: %q", f.screen.list.selected)
	}
}

func TestEditSendsChangedScope(t *testing.T) {
	f := newFixture(t, sampleKeys())
	f.show()
	f.selectRow(0)
	tapNamed(t, f.screen.Content(), "Edit…")
	overlay := f.overlay()
	groups := checkGroups(overlay)
	if !reflect.DeepEqual(groups[0].Selected, []string{"photos"}) || !reflect.DeepEqual(groups[1].Selected, []string{"Read", "List"}) {
		t.Fatalf("form not prefilled: buckets %v actions %v", groups[0].Selected, groups[1].Selected)
	}
	tapNamed(t, overlay, "Read and write")
	tapNamed(t, overlay, "Save changes")
	if len(f.client.updateReqs) != 1 {
		t.Fatalf("updates = %d", len(f.client.updateReqs))
	}
	req := f.client.updateReqs[0]
	if req.Name != nil || req.Scope == nil || len(req.Scope.Actions) != 4 {
		t.Fatalf("request = %+v", req)
	}
	if f.overlay() != nil {
		t.Fatalf("form still open")
	}
}

func TestEnterOpensEditor(t *testing.T) {
	f := newFixture(t, sampleKeys())
	f.show()
	f.selectRow(0)
	f.screen.list.table.TypedKey(&fyne.KeyEvent{Name: fyne.KeyReturn})
	if f.overlay() == nil || buttonNamed(f.overlay(), "Save changes") == nil {
		t.Fatalf("editor not opened")
	}
}

func TestShortcutModNOpensCreateForm(t *testing.T) {
	f := newFixture(t, sampleKeys())
	f.show()
	shortcut := &desktop.CustomShortcut{KeyName: fyne.KeyN, Modifier: fyne.KeyModifierShortcutDefault}
	handler := f.win.Canvas().(interface{ TypedShortcut(fyne.Shortcut) })
	handler.TypedShortcut(shortcut)
	if f.overlay() == nil || buttonNamed(f.overlay(), "Create key") == nil {
		t.Fatalf("Mod+N did not open the form")
	}
	f.win.Canvas().Overlays().Remove(f.overlay())
	f.screen.Hide()
	handler.TypedShortcut(shortcut)
	if f.overlay() != nil {
		t.Fatalf("Mod+N still bound after Hide")
	}
}

func TestUnauthenticatedRoutesToTelegram(t *testing.T) {
	client := sampleKeys()
	client.createErr = &adminv1.APIError{Status: http.StatusUnauthorized, Body: adminv1.ErrorBody{Code: adminv1.CodeUnauthenticated}}
	f := newFixture(t, client)
	f.show()
	createKey(t, f)
	if len(f.nav.routes) != 1 || f.nav.routes[0] != model.RouteTelegram {
		t.Fatalf("routes = %v", f.nav.routes)
	}
}

func TestScreenMetadata(t *testing.T) {
	f := newFixture(t, &fakeClient{})
	s := f.screen
	if s.Route() != model.RouteKeys || s.Title() != "Access keys" || s.Shortcut() != fyne.Key3 || s.Icon() == nil {
		t.Fatalf("metadata = %v %q %v", s.Route(), s.Title(), s.Shortcut())
	}
	if NewView(f.vm, f.nav) == nil {
		t.Fatalf("NewView returned nil")
	}
}
