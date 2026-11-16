package objects

import (
	"context"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/objects/vm"
)

const (
	dateLayout        = "2006-01-02 15:04"
	breadcrumbJoiner  = "›"
	noRowSelected     = -1
	closeGuardTitle   = "Transfers are running"
	keepOpenLabel     = "Keep open"
	quitAnywayLabel   = "Quit anyway"
	encryptedLabel    = "Encrypted"
	filterPlaceholder = "Filter by prefix"
	chooseBucketHint  = "Choose a bucket first."
)

type Options struct {
	Context     context.Context
	Notifier    shell.Notifier
	Clipboard   model.Clipboard
	Now         func() time.Time
	DownloadDir func() string
}

type browser struct {
	vmodel *vm.ViewModel
	nav    shell.Navigator
	win    fyne.Window
	opts   Options
	clip   model.Clipboard
	now    func() time.Time
	root   fyne.CanvasObject

	bucketSelect *widget.Select
	crumbs       *fyne.Container
	upButton     *widget.Button
	filter       *widget.Entry
	table        *browserTable
	details      *detailsPanel
	transfers    *transferPanel
	loadingBar   *widget.ProgressBarInfinite

	uiMu        sync.Mutex
	mu          sync.Mutex
	state       vm.State
	focusRow    int
	rendering   bool
	viewCtx     context.Context
	cancelView  context.CancelFunc
	cancelFocus context.CancelFunc
	lastProblem *model.Problem
	shortcuts   []*desktop.CustomShortcut
	previewKey  string
}

func newBrowser(v *vm.ViewModel, nav shell.Navigator, w fyne.Window, opts Options) *browser {
	if opts.Context == nil {
		opts.Context = context.Background()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	b := &browser{vmodel: v, nav: nav, win: w, opts: opts, now: opts.Now, focusRow: noRowSelected}
	b.clip = opts.Clipboard
	if b.clip == nil {
		b.clip = appClipboard{}
	}
	b.state = v.State().Get()
	b.build()
	v.State().Subscribe(func(st vm.State) { b.ui(func() { b.render(st) }) })
	b.transfers.bind(v)
	b.installCloseGuard()
	b.render(b.state)
	return b
}

func (b *browser) build() {
	b.bucketSelect = widget.NewSelect(nil, b.onBucketChosen)
	b.bucketSelect.PlaceHolder = "Bucket"
	b.crumbs = container.NewHBox()
	b.upButton = widget.NewButtonWithIcon("Up", theme.NavigateBackIcon(), func() { go b.vmodel.Up(b.commandContext()) })
	b.filter = widget.NewEntry()
	b.filter.SetPlaceHolder(filterPlaceholder)
	b.filter.OnSubmitted = func(text string) { go b.vmodel.ApplyFilter(b.commandContext(), strings.TrimSpace(text)) }
	b.loadingBar = widget.NewProgressBarInfinite()
	b.loadingBar.Hide()
	topBar := container.NewBorder(nil, nil, container.NewHBox(b.bucketSelect, b.upButton), nil, container.NewBorder(nil, nil, nil, container.NewGridWrap(fyne.NewSize(260, 36), b.filter), container.NewHScroll(b.crumbs)))

	b.table = newBrowserTable(tableHandlers{
		onUp:        func() { go b.vmodel.Up(b.commandContext()) },
		onDelete:    b.requestDelete,
		onActivate:  b.activateFocusedRow,
		onToggle:    b.toggleFocusedRow,
		onSelectAll: b.vmodel.SelectAll,
	})
	b.table.Length = b.tableSize
	b.table.CreateCell = b.createCell
	b.table.UpdateCell = b.updateCell
	b.table.CreateHeader = func() fyne.CanvasObject {
		return widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	}
	b.table.UpdateHeader = func(id widget.TableCellID, o fyne.CanvasObject) {
		if id.Col >= 0 && id.Col < columnCount {
			o.(*widget.Label).SetText(columnTitles[id.Col])
		}
	}
	for col, width := range columnWidths {
		b.table.SetColumnWidth(col, width)
	}
	b.table.OnSelected = b.onRowSelected

	b.details = newDetailsPanel(b.clip)
	b.transfers = newTransferPanel(b)
	toolbar := b.buildToolbar()
	main := container.NewBorder(container.NewVBox(topBar, toolbar, b.loadingBar), b.transfers.root, nil, nil, b.table)
	split := container.NewHSplit(main, b.details.root)
	split.SetOffset(0.68)
	b.root = split
}

func (b *browser) buildToolbar() fyne.CanvasObject {
	return container.NewHBox(
		widget.NewButtonWithIcon("Upload files…", theme.UploadIcon(), b.chooseFileToUpload),
		widget.NewButtonWithIcon("Download", theme.DownloadIcon(), b.startDownload),
		widget.NewButtonWithIcon("Delete…", theme.DeleteIcon(), b.requestDelete),
		widget.NewButtonWithIcon("Copy presigned URL…", theme.ContentCopyIcon(), b.openPresignDialog),
		widget.NewButtonWithIcon("Refresh", theme.ViewRefreshIcon(), func() { go b.vmodel.Refresh(b.commandContext()) }),
	)
}

func (b *browser) ui(fn func()) {
	fyne.Do(func() {
		b.uiMu.Lock()
		defer b.uiMu.Unlock()
		fn()
	})
}

func (b *browser) commandContext() context.Context {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.viewCtx != nil {
		return b.viewCtx
	}
	return b.opts.Context
}

func (b *browser) transferContext() context.Context {
	return b.opts.Context
}

func (b *browser) snapshot() vm.State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

func (b *browser) tableSize() (int, int) {
	return len(b.snapshot().Rows), columnCount
}

func (b *browser) render(st vm.State) {
	b.mu.Lock()
	moved := b.state.Bucket != st.Bucket || b.state.Prefix != st.Prefix
	if moved {
		b.focusRow = noRowSelected
	}
	b.state = st
	b.rendering = true
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.rendering = false
		b.mu.Unlock()
	}()
	b.renderToolbarArea(st)
	if moved {
		b.table.UnselectAll()
	}
	b.table.Refresh()
	b.details.render(st)
	b.renderPreview(st)
	b.presentNewProblem(st)
}

func (b *browser) renderToolbarArea(st vm.State) {
	b.bucketSelect.SetOptions(st.Buckets)
	if st.Bucket != "" && b.bucketSelect.Selected != st.Bucket {
		b.bucketSelect.SetSelected(st.Bucket)
	}
	if st.Loading || st.LoadingMore {
		b.loadingBar.Show()
		b.loadingBar.Start()
	} else {
		b.loadingBar.Stop()
		b.loadingBar.Hide()
	}
	if st.Prefix == "" {
		b.upButton.Disable()
	} else {
		b.upButton.Enable()
	}
	if b.filter.Text != st.Filter && !b.isFocused(b.filter) {
		b.filter.SetText(st.Filter)
	}
	b.renderCrumbs(st)
}

func (b *browser) isFocused(o fyne.Focusable) bool {
	return b.win.Canvas().Focused() == o
}

func (b *browser) renderCrumbs(st vm.State) {
	b.crumbs.Objects = nil
	if st.Bucket == "" {
		b.crumbs.Refresh()
		return
	}
	for i, crumb := range st.Breadcrumbs {
		if i > 0 {
			b.crumbs.Add(widget.NewLabel(breadcrumbJoiner))
		}
		prefix := crumb.Prefix
		button := widget.NewButton(crumb.Label, func() { go b.vmodel.Open(b.commandContext(), st.Bucket, prefix) })
		button.Importance = widget.LowImportance
		b.crumbs.Add(button)
	}
	b.crumbs.Refresh()
}

func (b *browser) onBucketChosen(bucket string) {
	b.mu.Lock()
	skip := b.rendering || bucket == b.state.Bucket
	b.mu.Unlock()
	if skip || bucket == "" {
		return
	}
	go b.vmodel.SelectBucket(b.commandContext(), bucket)
}

func (b *browser) presentNewProblem(st vm.State) {
	b.mu.Lock()
	same := st.Problem == b.lastProblem
	b.lastProblem = st.Problem
	b.mu.Unlock()
	if same || st.Problem == nil {
		return
	}
	b.present(*st.Problem)
}

func (b *browser) present(p model.Problem) {
	if p.Code == "" && p.Title == "" {
		return
	}
	if b.opts.Notifier != nil {
		b.opts.Notifier.Problem(p)
		return
	}
	dlg := newInfoDialog("", shell.NewProblemView(p, b.clip), b.win)
	dlg.Show()
	if p.Presentation == model.PresentRoute && p.Route != "" {
		b.nav.Navigate(p.Route, nil)
	}
}

func (b *browser) inform(text string) {
	if b.opts.Notifier != nil {
		b.opts.Notifier.Info(text)
		return
	}
	newInfoDialog("", widget.NewLabel(text), b.win).Show()
}

func (b *browser) bindShortcuts() {
	b.unbindShortcuts()
	canvas := b.win.Canvas()
	add := func(key fyne.KeyName, mod fyne.KeyModifier, fn func()) {
		sc := &desktop.CustomShortcut{KeyName: key, Modifier: mod}
		b.shortcuts = append(b.shortcuts, sc)
		canvas.AddShortcut(sc, func(fyne.Shortcut) { fn() })
	}
	add(fyne.KeyU, fyne.KeyModifierShortcutDefault, b.chooseFileToUpload)
	add(fyne.KeyD, fyne.KeyModifierShortcutDefault, b.startDownload)
	add(fyne.KeyL, fyne.KeyModifierShortcutDefault, func() { canvas.Focus(b.filter) })
	add(fyne.KeyLeft, fyne.KeyModifierAlt, func() { go b.vmodel.Up(b.commandContext()) })
	b.win.SetOnDropped(func(_ fyne.Position, uris []fyne.URI) { b.handleDrop(uris) })
}

func (b *browser) unbindShortcuts() {
	canvas := b.win.Canvas()
	for _, sc := range b.shortcuts {
		canvas.RemoveShortcut(sc)
	}
	b.shortcuts = nil
	b.win.SetOnDropped(nil)
}

func (b *browser) show(ctx context.Context, p model.Params) {
	viewCtx, cancel := context.WithCancel(ctx)
	b.mu.Lock()
	if b.cancelView != nil {
		b.cancelView()
	}
	b.viewCtx, b.cancelView = viewCtx, cancel
	b.mu.Unlock()
	b.bindShortcuts()
	go b.load(viewCtx, p)
}

func (b *browser) load(ctx context.Context, p model.Params) {
	bucket := p[model.ParamBucket]
	if bucket == "" {
		b.vmodel.Refresh(ctx)
		return
	}
	b.vmodel.LoadBuckets(ctx)
	b.vmodel.Open(ctx, bucket, p[model.ParamPrefix])
}

func (b *browser) hide() {
	b.unbindShortcuts()
	b.mu.Lock()
	cancel, cancelFocus := b.cancelView, b.cancelFocus
	b.cancelView, b.viewCtx, b.cancelFocus = nil, nil, nil
	b.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if cancelFocus != nil {
		cancelFocus()
	}
}

func (b *browser) installCloseGuard() {
	b.win.SetCloseIntercept(func() {
		active := b.vmodel.ActiveTransfers()
		if active == 0 {
			b.win.Close()
			return
		}
		b.askQuitWithTransfers(active)
	})
}
