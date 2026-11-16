package keys

import (
	"slices"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/keys/vm"
)

const (
	allBucketsLabel = "All buckets, including future ones"
	presetReadOnly  = "Read only"
	presetReadWrite = "Read and write"
)

type keyForm struct {
	clip       model.Clipboard
	name       *widget.Entry
	all        *widget.Check
	buckets    *widget.CheckGroup
	actions    *widget.CheckGroup
	nameError  *widget.Label
	scopeError *widget.Label
	formError  *fyne.Container
	progress   *widget.ProgressBarInfinite
	root       fyne.CanvasObject
}

func newKeyForm(name string, scope vm.KeyScope, known []string, clip model.Clipboard) *keyForm {
	f := &keyForm{clip: clip}
	f.name = widget.NewEntry()
	f.name.SetPlaceHolder("website prod")
	f.all = widget.NewCheck(allBucketsLabel, func(bool) { f.syncBucketList() })
	f.buckets = widget.NewCheckGroup(nil, func([]string) { f.scopeError.SetText("") })
	f.actions = widget.NewCheckGroup(actionWords(), func([]string) { f.scopeError.SetText("") })
	f.nameError = newErrorLabel()
	f.scopeError = newErrorLabel()
	f.formError = container.NewStack()
	f.progress = widget.NewProgressBarInfinite()
	f.progress.Hide()
	f.name.OnChanged = func(string) { f.nameError.SetText("") }
	f.fill(name, scope, known)
	f.root = f.layout()
	return f
}

func newErrorLabel() *widget.Label {
	l := widget.NewLabel("")
	l.Wrapping = fyne.TextWrapWord
	l.Importance = widget.DangerImportance
	return l
}

func actionWords() []string {
	all := vm.AllActions()
	words := make([]string, len(all))
	for i, a := range all {
		words[i] = vm.ActionWord(a)
	}
	return words
}

func (f *keyForm) fill(name string, scope vm.KeyScope, known []string) {
	f.name.SetText(name)
	f.setBucketOptions(known, scope.Buckets)
	f.buckets.SetSelected(scope.Buckets)
	f.all.SetChecked(scope.AllBuckets)
	f.setActions(scope.Actions)
	f.syncBucketList()
}

func (f *keyForm) layout() fyne.CanvasObject {
	presets := shell.Actions(
		widget.NewButton(presetReadOnly, func() { f.setActions(vm.PresetReadOnly()) }),
		widget.NewButton(presetReadWrite, func() { f.setActions(vm.PresetReadWrite()) }),
	)
	return container.NewVBox(
		shell.NewFieldLabel("Name"), f.name, f.nameError,
		shell.NewFieldLabel("Buckets"), f.all, f.buckets,
		shell.NewFieldLabel("Permissions"), f.actions, presets,
		f.scopeError, f.formError, f.progress,
	)
}

func (f *keyForm) syncBucketList() {
	if f.all.Checked {
		f.buckets.Disable()
	} else {
		f.buckets.Enable()
	}
	f.scopeError.SetText("")
}

func (f *keyForm) setBucketOptions(known, selected []string) {
	options := slices.Clone(known)
	for _, name := range selected {
		if !slices.Contains(options, name) {
			options = append(options, name)
		}
	}
	slices.Sort(options)
	if slices.Equal(options, f.buckets.Options) {
		return
	}
	chosen := slices.Clone(f.buckets.Selected)
	f.buckets.Options = options
	f.buckets.Refresh()
	f.buckets.SetSelected(chosen)
}

func (f *keyForm) setActions(actions []string) {
	words := make([]string, len(actions))
	for i, a := range actions {
		words[i] = vm.ActionWord(a)
	}
	f.actions.SetSelected(words)
}

func (f *keyForm) selectedActions() []string {
	var out []string
	for _, a := range vm.AllActions() {
		if slices.Contains(f.actions.Selected, vm.ActionWord(a)) {
			out = append(out, a)
		}
	}
	return out
}

func (f *keyForm) scope() vm.KeyScope {
	return vm.KeyScope{
		AllBuckets: f.all.Checked,
		Buckets:    slices.Clone(f.buckets.Selected),
		Actions:    f.selectedActions(),
	}
}

func (f *keyForm) newKey() vm.NewKey {
	return vm.NewKey{Name: strings.TrimSpace(f.name.Text), Scope: f.scope()}
}

func (f *keyForm) keyEdit() vm.KeyEdit {
	return vm.KeyEdit{Name: strings.TrimSpace(f.name.Text), Scope: f.scope()}
}

func (f *keyForm) clearErrors() {
	f.nameError.SetText("")
	f.scopeError.SetText("")
	f.formError.Objects = nil
	f.formError.Refresh()
}

func (f *keyForm) setBusy(busy bool) {
	if busy {
		f.clearErrors()
		f.progress.Show()
		f.progress.Start()
		return
	}
	f.progress.Stop()
	f.progress.Hide()
}

func (f *keyForm) showProblem(p *model.Problem) {
	f.clearErrors()
	switch {
	case p.Presentation == model.PresentInline && p.Field == vm.FieldName:
		f.nameError.SetText(inlineText(p))
	case p.Presentation == model.PresentInline && p.Field == vm.FieldScope:
		f.scopeError.SetText(inlineText(p))
	default:
		f.formError.Objects = []fyne.CanvasObject{shell.NewProblemView(*p, f.clip)}
		f.formError.Refresh()
	}
}
