package settings

import (
	"strconv"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/settings/vm"
)

type preferencesPanel struct {
	w           *view
	root        fyne.CanvasObject
	theme       *widget.Select
	downloadDir *widget.Entry
	concurrency *widget.Select
	clipboard   *widget.Entry
	syncing     bool
	synced      vm.PreferencesState
	hasSynced   bool
}

func newPreferencesPanel(w *view) *preferencesPanel {
	p := &preferencesPanel{w: w}
	p.theme = widget.NewSelect(vm.ThemeModes(), nil)
	p.theme.OnChanged = p.onTheme
	p.downloadDir = widget.NewEntry()
	p.downloadDir.OnSubmitted = func(string) { p.onDownloadDir() }
	save := widget.NewButtonWithIcon("Save folder", theme.DocumentSaveIcon(), p.onDownloadDir)
	browse := widget.NewButtonWithIcon("Browse…", theme.FolderOpenIcon(), p.onBrowse)
	p.concurrency = widget.NewSelect([]string{"1", "2", "3", "4"}, nil)
	p.concurrency.OnChanged = p.onConcurrency
	p.clipboard = widget.NewEntry()
	p.clipboard.OnSubmitted = func(string) { p.onClipboard() }
	apply := widget.NewButtonWithIcon("Apply", theme.ConfirmIcon(), p.onClipboard)
	form := container.NewVBox(
		widget.NewLabel("Theme"),
		container.NewHBox(shell.FramedInput(p.theme)),
		shell.NewSectionGap(),
		widget.NewLabel("Download folder"),
		container.NewBorder(nil, nil, nil, shell.Actions(browse, save), p.downloadDir),
		shell.NewSectionGap(),
		widget.NewLabel("Upload concurrency (1 to 4)"),
		container.NewHBox(shell.FramedInput(p.concurrency)),
		shell.NewSectionGap(),
		widget.NewLabel("Clear the clipboard after this many seconds (0 never clears)"),
		container.NewBorder(nil, nil, nil, shell.Action(apply), p.clipboard),
	)
	p.root = scrolledCard(stepPreferences, "App preferences", form)
	return p
}

func (p *preferencesPanel) object() fyne.CanvasObject { return p.root }

func (p *preferencesPanel) update(s vm.State) {
	prefs := s.Preferences
	if p.hasSynced && p.synced == prefs {
		return
	}
	p.syncing = true
	defer func() { p.syncing = false }()
	p.synced, p.hasSynced = prefs, true
	p.theme.SetSelected(prefs.Theme)
	p.downloadDir.SetText(prefs.DownloadDir)
	p.concurrency.SetSelected(strconv.Itoa(prefs.UploadConcurrency))
	p.clipboard.SetText(strconv.Itoa(prefs.ClipboardClearSeconds))
}

func (p *preferencesPanel) onTheme(mode string) {
	if p.syncing {
		return
	}
	if p.w.vm.SetTheme(mode) {
		fyne.CurrentApp().Settings().SetTheme(ThemeFor(mode))
	}
}

func (p *preferencesPanel) onDownloadDir() {
	p.w.vm.SetDownloadDir(p.downloadDir.Text)
}

func (p *preferencesPanel) onConcurrency(text string) {
	if p.syncing {
		return
	}
	n, err := strconv.Atoi(text)
	if err != nil {
		return
	}
	p.w.vm.SetUploadConcurrency(n)
}

func (p *preferencesPanel) onClipboard() {
	n, err := strconv.Atoi(p.clipboard.Text)
	if err != nil {
		n = -1
	}
	p.w.vm.SetClipboardClearSeconds(n)
}

func (p *preferencesPanel) onBrowse() {
	win := p.w.window()
	if win == nil {
		return
	}
	picker := dialog.NewFolderOpen(func(dir fyne.ListableURI, err error) {
		if err != nil || dir == nil {
			return
		}
		p.downloadDir.SetText(dir.Path())
		p.onDownloadDir()
	}, win)
	if start, err := storage.ListerForURI(storage.NewFileURI(p.downloadDir.Text)); err == nil {
		picker.SetLocation(start)
	}
	picker.Show()
}
