package buckets

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
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/buckets/vm"
)

var testNow = time.Date(2026, 11, 16, 9, 30, 0, 0, time.UTC)

type fakeClipboard struct{ text string }

func (c *fakeClipboard) SetText(text string) { c.text = text }
func (c *fakeClipboard) Text() string        { return c.text }

type fakeNav struct {
	routes []model.Route
	params []model.Params
}

func (n *fakeNav) Navigate(r model.Route, p model.Params) {
	n.routes = append(n.routes, r)
	n.params = append(n.params, p)
}
func (n *fakeNav) Current() model.Route                 { return "" }
func (n *fakeNav) SetEnabled(model.Route, bool, string) {}

type fakeClient struct {
	buckets   []adminv1.Bucket
	createErr error
	deleteErr error
	created   []adminv1.CreateBucketRequest
	updated   []adminv1.UpdateBucketRequest
	deleted   []string
}

func (f *fakeClient) ListBuckets(context.Context) (adminv1.BucketList, error) {
	return adminv1.BucketList{Buckets: append([]adminv1.Bucket(nil), f.buckets...)}, nil
}

func (f *fakeClient) CreateBucket(_ context.Context, req adminv1.CreateBucketRequest) (adminv1.Bucket, error) {
	f.created = append(f.created, req)
	if f.createErr != nil {
		return adminv1.Bucket{}, f.createErr
	}
	f.buckets = append(f.buckets, adminv1.Bucket{Name: req.Name})
	return adminv1.Bucket{Name: req.Name}, nil
}

func (f *fakeClient) GetBucket(context.Context, string) (adminv1.Bucket, error) {
	return adminv1.Bucket{}, nil
}

func (f *fakeClient) UpdateBucket(_ context.Context, name string, req adminv1.UpdateBucketRequest) (adminv1.Bucket, error) {
	f.updated = append(f.updated, req)
	return adminv1.Bucket{Name: name}, nil
}

func (f *fakeClient) DeleteBucket(_ context.Context, name string) error {
	f.deleted = append(f.deleted, name)
	return f.deleteErr
}

type fixture struct {
	client *fakeClient
	vm     *vm.ViewModel
	nav    *fakeNav
	clip   *fakeClipboard
	screen *Screen
	win    fyne.Window
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
	})
	f.win = test.NewTempWindow(t, f.screen.Content())
	f.win.Resize(fyne.NewSize(1300, 700))
	return f
}

func (f *fixture) show() {
	f.screen.Show(context.Background(), nil)
}

func (f *fixture) overlay() fyne.CanvasObject {
	return f.win.Canvas().Overlays().Top()
}

func (f *fixture) selectRow(row int) {
	f.screen.list.table.Select(widget.TableCellID{Row: row, Col: 0})
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

func (f *fixture) typeInFocused(text string) {
	focused, ok := f.win.Canvas().Focused().(fyne.Focusable)
	if !ok || focused == nil {
		panic("nothing focused")
	}
	test.Type(focused, text)
}

func (f *fixture) cell(row, col int) string {
	label := widget.NewLabel("")
	f.screen.list.updateCell(widget.TableCellID{Row: row, Col: col}, label)
	return label.Text
}

func (f *fixture) header(col int) *widget.Button {
	b := widget.NewButton("", nil)
	f.screen.list.updateHeader(widget.TableCellID{Row: -1, Col: col}, b)
	return b
}

func hasLabelContaining(root fyne.CanvasObject, text string) bool {
	for _, o := range descend(root) {
		if l, ok := o.(*widget.Label); ok && strings.Contains(l.Text, text) {
			return true
		}
	}
	return false
}

func tapNamed(t *testing.T, root fyne.CanvasObject, text string) {
	t.Helper()
	b := buttonNamed(root, text)
	if b == nil {
		t.Fatalf("no button %q", text)
	}
	test.Tap(b)
}

func twoBuckets() *fakeClient {
	return &fakeClient{buckets: []adminv1.Bucket{
		{Name: "photos", Policy: "public-read", EncryptionDefault: "tde1", CacheMaxAgeSeconds: 86400, ObjectCount: 12, StoredBytes: 2048, CreatedAt: testNow.Add(-time.Hour)},
		{Name: "avatars", Policy: "private", EncryptionDefault: "none", CacheMaxAgeSeconds: 3600, ObjectCount: 0},
	}}
}

func TestListRendersRowsAndHeaders(t *testing.T) {
	f := newFixture(t, twoBuckets())
	f.show()
	want := [][]string{
		{"avatars", "private", "Off", "1 h", "0", "0 B", ""},
		{"photos", "public-read", "On", "1 day", "12", "2.0 KiB", "2026-11-16 08:30"},
	}
	for row, cells := range want {
		for col, text := range cells {
			if got := f.cell(row, col); got != text {
				t.Errorf("cell(%d,%d) = %q, want %q", row, col, got, text)
			}
		}
	}
	headers := []string{"Name (ascending)", "Access", "Encryption", "Cache max-age", "Objects", "Size", "Created"}
	for col, text := range headers {
		if got := f.header(col).Text; got != text {
			t.Errorf("header %d = %q, want %q", col, got, text)
		}
	}
	if !f.screen.empty.Hidden {
		t.Errorf("empty message shown with buckets present")
	}
}

func TestEmptyListShowsHint(t *testing.T) {
	f := newFixture(t, &fakeClient{})
	f.show()
	if f.screen.empty.Hidden {
		t.Fatalf("empty message hidden for an empty list")
	}
}

func TestActionsNeedASelection(t *testing.T) {
	f := newFixture(t, twoBuckets())
	f.show()
	for _, name := range []string{"Settings…", "Browse objects", "Delete…"} {
		if !buttonNamed(f.screen.Content(), name).Disabled() {
			t.Errorf("%q enabled without a selection", name)
		}
	}
	f.selectRow(0)
	for _, name := range []string{"Settings…", "Browse objects", "Delete…"} {
		if buttonNamed(f.screen.Content(), name).Disabled() {
			t.Errorf("%q disabled with a selection", name)
		}
	}
}

func TestHeaderTapSortsColumn(t *testing.T) {
	f := newFixture(t, twoBuckets())
	f.show()
	test.Tap(f.header(4))
	got := f.vm.State().Get()
	if got.SortColumn != vm.ColumnObjects || got.Buckets[0].Name != "avatars" {
		t.Fatalf("sort = %v, first = %q", got.SortColumn, got.Buckets[0].Name)
	}
	test.Tap(f.header(4))
	if !f.vm.State().Get().SortDescending {
		t.Fatalf("second tap did not reverse the order")
	}
}

func TestBrowseOpensObjectsWithBucket(t *testing.T) {
	f := newFixture(t, twoBuckets())
	f.show()
	f.selectRow(1)
	tapNamed(t, f.screen.Content(), "Browse objects")
	if len(f.nav.routes) != 1 || f.nav.routes[0] != model.RouteObjects || f.nav.params[0][model.ParamBucket] != "photos" {
		t.Fatalf("navigation = %v %v", f.nav.routes, f.nav.params)
	}
}

func TestEnterOpensSelectedBucket(t *testing.T) {
	f := newFixture(t, twoBuckets())
	f.show()
	f.selectRow(0)
	f.screen.list.table.TypedKey(&fyne.KeyEvent{Name: fyne.KeyReturn})
	if len(f.nav.routes) != 1 || f.nav.params[0][model.ParamBucket] != "avatars" {
		t.Fatalf("navigation = %v %v", f.nav.routes, f.nav.params)
	}
}

func TestArrowKeysMoveSelection(t *testing.T) {
	f := newFixture(t, twoBuckets())
	f.show()
	f.screen.list.table.TypedKey(&fyne.KeyEvent{Name: fyne.KeyDown})
	if f.screen.list.selected != "avatars" {
		t.Fatalf("selected = %q", f.screen.list.selected)
	}
	f.screen.list.table.TypedKey(&fyne.KeyEvent{Name: fyne.KeyDown})
	f.screen.list.table.TypedKey(&fyne.KeyEvent{Name: fyne.KeyDown})
	if f.screen.list.selected != "photos" {
		t.Fatalf("selected = %q after moving past the end", f.screen.list.selected)
	}
	f.screen.list.table.TypedKey(&fyne.KeyEvent{Name: fyne.KeyUp})
	if f.screen.list.selected != "avatars" {
		t.Fatalf("selected = %q after moving up", f.screen.list.selected)
	}
}

func TestDoubleTapOpensSelectedBucket(t *testing.T) {
	f := newFixture(t, twoBuckets())
	f.show()
	f.selectRow(0)
	test.DoubleTap(f.screen.list.table)
	if len(f.nav.routes) != 1 || f.nav.routes[0] != model.RouteObjects {
		t.Fatalf("navigation = %v", f.nav.routes)
	}
}

func TestCreateDialogCreatesBucket(t *testing.T) {
	f := newFixture(t, &fakeClient{})
	f.show()
	tapNamed(t, f.screen.Content(), "New bucket")
	overlay := f.overlay()
	if overlay == nil {
		t.Fatalf("no dialog opened")
	}
	f.typeInFocused("photos")
	tapNamed(t, overlay, "Create bucket")
	if len(f.client.created) != 1 || f.client.created[0].Name != "photos" {
		t.Fatalf("created = %+v", f.client.created)
	}
	if f.client.created[0].Policy != vm.PolicyPrivate || f.client.created[0].EncryptionDefault != vm.EncryptionNone {
		t.Fatalf("defaults = %+v", f.client.created[0])
	}
	if f.overlay() != nil {
		t.Fatalf("dialog still open after success")
	}
	if len(f.vm.State().Get().Buckets) != 1 {
		t.Fatalf("list not refreshed")
	}
}

func TestCreateDialogStaysOpenOnServerError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{name: "exists", err: &adminv1.APIError{Status: http.StatusConflict, Body: adminv1.ErrorBody{Code: adminv1.CodeBucketExists}}, want: "A bucket with this name exists."},
		{name: "limit", err: &adminv1.APIError{Status: http.StatusConflict, Body: adminv1.ErrorBody{Code: adminv1.CodeBucketLimitReached, Details: map[string]any{"limit": float64(3)}}}, want: "at most 3 buckets"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, &fakeClient{createErr: tc.err})
			f.show()
			tapNamed(t, f.screen.Content(), "New bucket")
			f.typeInFocused("photos")
			tapNamed(t, f.overlay(), "Create bucket")
			if f.overlay() == nil {
				t.Fatalf("dialog closed after an error")
			}
			if !hasLabelContaining(f.overlay(), tc.want) {
				t.Fatalf("dialog does not show %q", tc.want)
			}
			if buttonNamed(f.overlay(), "Create bucket").Disabled() {
				t.Fatalf("submit stays disabled after the response")
			}
			if st := f.vm.State().Get(); st.Problem != nil || st.FormProblem != nil {
				t.Fatalf("problem not moved into the dialog: %+v", st)
			}
		})
	}
}

func TestCreateDialogRejectsBadNameBeforeSending(t *testing.T) {
	f := newFixture(t, &fakeClient{})
	f.show()
	tapNamed(t, f.screen.Content(), "New bucket")
	f.typeInFocused("Bad_Name")
	tapNamed(t, f.overlay(), "Create bucket")
	if len(f.client.created) != 0 {
		t.Fatalf("invalid name was sent")
	}
	if !hasLabelContaining(f.overlay(), "lowercase letters") {
		t.Fatalf("reason not shown")
	}
}

func TestCreateDialogNotices(t *testing.T) {
	f := newFixture(t, &fakeClient{})
	f.show()
	tapNamed(t, f.screen.Content(), "New bucket")
	overlay := f.overlay()
	if hasLabelContaining(overlay, publicReadNotice) || hasLabelContaining(overlay, "server key file") {
		t.Fatalf("notices shown before a choice")
	}
	for _, o := range descend(overlay) {
		if r, ok := o.(*widget.RadioGroup); ok {
			if r.Selected == accessPrivate {
				r.SetSelected(accessPublicRead)
			} else {
				r.SetSelected(encryptionOn)
			}
		}
	}
	if !hasLabelContaining(overlay, publicReadNotice) {
		t.Errorf("public read notice missing")
	}
	if !hasLabelContaining(overlay, "Back it up off the host") {
		t.Errorf("encryption notice missing")
	}
}

func TestPresetFillsCacheSeconds(t *testing.T) {
	form := newBucketForm(vm.NewBucket{CacheMaxAgeSeconds: 10}, true, &fakeClipboard{})
	form.preset.SetSelected("7 days")
	if form.cache.Text != "604800" {
		t.Fatalf("cache = %q", form.cache.Text)
	}
	form.cache.SetText("abc")
	if form.newBucket().CacheMaxAgeSeconds >= 0 {
		t.Fatalf("a non number was accepted")
	}
	form.cache.SetText("")
	if form.newBucket().CacheMaxAgeSeconds != vm.DefaultCacheMaxAgeSeconds {
		t.Fatalf("empty cache did not fall back to the default")
	}
}

func TestSettingsDialogUpdatesBucket(t *testing.T) {
	f := newFixture(t, twoBuckets())
	f.show()
	f.selectRow(0)
	tapNamed(t, f.screen.Content(), "Settings…")
	overlay := f.overlay()
	if !hasLabelContaining(overlay, "apply to new objects only") {
		t.Fatalf("encryption note missing")
	}
	for _, o := range descend(overlay) {
		if r, ok := o.(*widget.RadioGroup); ok && r.Selected == accessPrivate {
			r.SetSelected(accessPublicRead)
		}
	}
	tapNamed(t, overlay, "Save settings")
	if len(f.client.updated) != 1 || f.client.updated[0].Policy == nil || *f.client.updated[0].Policy != vm.PolicyPublicRead {
		t.Fatalf("updated = %+v", f.client.updated)
	}
	if f.overlay() != nil {
		t.Fatalf("dialog still open")
	}
}

func TestDeleteNeedsTypedName(t *testing.T) {
	f := newFixture(t, twoBuckets())
	f.show()
	f.selectRow(1)
	tapNamed(t, f.screen.Content(), "Delete…")
	overlay := f.overlay()
	confirm := buttonNamed(overlay, "Delete bucket")
	if confirm == nil || !confirm.Disabled() {
		t.Fatalf("confirm button missing or enabled before typing")
	}
	f.typeInFocused("phot")
	if !buttonNamed(overlay, "Delete bucket").Disabled() {
		t.Fatalf("confirm enabled for a partial name")
	}
	f.typeInFocused("os")
	tapNamed(t, overlay, "Delete bucket")
	if len(f.client.deleted) != 1 || f.client.deleted[0] != "photos" {
		t.Fatalf("deleted = %v", f.client.deleted)
	}
}

func TestDeleteNotEmptyShowsDescriptionAndOffersBrowser(t *testing.T) {
	client := twoBuckets()
	client.deleteErr = &adminv1.APIError{Status: http.StatusConflict, Body: adminv1.ErrorBody{
		Code: adminv1.CodeBucketNotEmpty, Message: "bucket not empty", Details: map[string]any{"object_count": float64(12)},
	}}
	f := newFixture(t, client)
	f.show()
	f.selectRow(1)
	tapNamed(t, f.screen.Content(), "Delete…")
	f.typeInFocused("photos")
	tapNamed(t, f.overlay(), "Delete bucket")
	overlay := f.overlay()
	if overlay == nil {
		t.Fatalf("no not-empty dialog")
	}
	if !hasLabelContaining(overlay, "12 objects remain. Delete them first.") {
		t.Fatalf("description missing")
	}
	if hasLabelContaining(overlay, "bucket not empty") {
		t.Fatalf("raw server message leaked")
	}
	tapNamed(t, overlay, "Open in object browser")
	if len(f.nav.routes) != 1 || f.nav.routes[0] != model.RouteObjects || f.nav.params[0][model.ParamBucket] != "photos" {
		t.Fatalf("navigation = %v %v", f.nav.routes, f.nav.params)
	}
	if f.overlay() != nil {
		t.Fatalf("dialog still open")
	}
	if f.vm.State().Get().NotEmpty != nil {
		t.Fatalf("not-empty state kept after closing")
	}
}

func TestShortcutModNOpensCreateDialog(t *testing.T) {
	f := newFixture(t, &fakeClient{})
	f.show()
	shortcut := &desktop.CustomShortcut{KeyName: fyne.KeyN, Modifier: fyne.KeyModifierShortcutDefault}
	f.win.Canvas().(interface{ TypedShortcut(fyne.Shortcut) }).TypedShortcut(shortcut)
	if f.overlay() == nil {
		t.Fatalf("Mod+N did not open the create dialog")
	}
	f.win.Canvas().Overlays().Remove(f.overlay())
	f.screen.Hide()
	f.win.Canvas().(interface{ TypedShortcut(fyne.Shortcut) }).TypedShortcut(shortcut)
	if f.overlay() != nil {
		t.Fatalf("Mod+N still bound after Hide")
	}
}

func TestRouteProblemNavigatesOnce(t *testing.T) {
	f := newFixture(t, &fakeClient{createErr: &adminv1.APIError{Status: http.StatusUnauthorized, Body: adminv1.ErrorBody{Code: adminv1.CodeUnauthenticated}}})
	f.show()
	in := vm.DefaultNewBucket()
	in.Name = "photos"
	f.vm.Create(context.Background(), in)
	f.vm.Refresh(context.Background())
	if len(f.nav.routes) != 1 || f.nav.routes[0] != model.RouteTelegram {
		t.Fatalf("routes = %v", f.nav.routes)
	}
}

func TestScreenMetadata(t *testing.T) {
	f := newFixture(t, &fakeClient{})
	s := f.screen
	if s.Route() != model.RouteBuckets || s.Title() != "Buckets" || s.Shortcut() != fyne.Key2 || s.Icon() == nil {
		t.Fatalf("metadata = %v %q %v", s.Route(), s.Title(), s.Shortcut())
	}
	if NewView(f.vm, f.nav) == nil {
		t.Fatalf("NewView returned nil")
	}
}
