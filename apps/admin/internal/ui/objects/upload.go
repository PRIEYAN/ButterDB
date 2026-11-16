package objects

import (
	"context"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/objects/vm"
)

const (
	encryptionDefaultLabel = "Bucket default"
	encryptionOnLabel      = "Encrypt"
	encryptionOffLabel     = "Do not encrypt"
	fileURIScheme          = "file"
)

func (b *browser) prompts() vm.Prompts {
	return vm.Prompts{Replace: b.askReplace, Conflict: b.askConflict}
}

func (b *browser) chooseFileToUpload() {
	bucket, prefix := b.vmodel.Location()
	if bucket == "" {
		b.inform(chooseBucketHint)
		return
	}
	dlg := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
		if err != nil {
			b.present(model.Describe(err, b.now()))
			return
		}
		if reader == nil {
			return
		}
		path := reader.URI().Path()
		_ = reader.Close()
		go b.prepareSingleUpload(bucket, prefix, path)
	}, b.win)
	dlg.Show()
}

func (b *browser) prepareSingleUpload(bucket, prefix, path string) {
	spec, err := b.vmodel.PrepareUpload(prefix, path)
	if err != nil {
		b.ui(func() { b.present(model.Describe(err, b.now())) })
		return
	}
	b.ui(func() { b.showUploadOptions(bucket, spec) })
}

func (b *browser) showUploadOptions(bucket string, spec vm.UploadSpec) {
	key := widget.NewEntry()
	key.SetText(spec.Key)
	contentType := widget.NewEntry()
	contentType.SetText(spec.ContentType)
	encryption := widget.NewSelect([]string{encryptionDefaultLabel, encryptionOnLabel, encryptionOffLabel}, nil)
	encryption.SetSelected(encryptionDefaultLabel)
	metadata := widget.NewMultiLineEntry()
	metadata.SetPlaceHolder("name=value, one pair per line")
	metadata.SetMinRowsVisible(3)
	problem := widget.NewLabel("")
	problem.Wrapping = fyne.TextWrapWord
	form := widget.NewForm(
		widget.NewFormItem("Key", key),
		widget.NewFormItem("Content type", contentType),
		widget.NewFormItem("Encryption", encryption),
		widget.NewFormItem("Metadata", metadata),
	)
	var dlg *shell.Dialog
	cancel := widget.NewButton("Cancel", func() { dlg.Hide() })
	upload := widget.NewButton("Upload", func() {
		pairs, bad := vm.ParseMetadata(metadata.Text)
		if bad != nil {
			problem.SetText(bad.Title)
			return
		}
		spec.Key = key.Text
		spec.ContentType = contentType.Text
		spec.Encryption = encryptionValue(encryption.Selected)
		spec.Metadata = pairs
		dlg.Hide()
		b.startUploads(bucket, []vm.UploadSpec{spec})
	})
	upload.Importance = widget.HighImportance
	dlg = newChoiceDialog(b.win, "objects/upload-options", "Upload options", container.NewVBox(form, problem), cancel, upload)
	dlg.Show()
	b.win.Canvas().Focus(key)
}

func encryptionValue(label string) string {
	switch label {
	case encryptionOnLabel:
		return vm.EncryptionOn
	case encryptionOffLabel:
		return vm.EncryptionOff
	}
	return vm.EncryptionBucketDefault
}

func (b *browser) startUploads(bucket string, specs []vm.UploadSpec) {
	ctx := b.transferContext()
	go b.vmodel.Upload(ctx, bucket, specs, b.prompts())
}

func (b *browser) handleDrop(uris []fyne.URI) {
	bucket, prefix := b.vmodel.Location()
	if bucket == "" {
		b.inform(chooseBucketHint)
		return
	}
	paths := make([]string, 0, len(uris))
	for _, uri := range uris {
		if uri.Scheme() == fileURIScheme {
			paths = append(paths, uri.Path())
		}
	}
	if len(paths) == 0 {
		return
	}
	go b.uploadPaths(bucket, prefix, paths)
}

func (b *browser) uploadPaths(bucket, prefix string, paths []string) {
	specs := make([]vm.UploadSpec, 0, len(paths))
	var firstErr error
	for _, path := range paths {
		spec, err := b.vmodel.PrepareUpload(prefix, path)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		specs = append(specs, spec)
	}
	if firstErr != nil {
		b.ui(func() { b.present(model.Describe(firstErr, b.now())) })
	}
	if len(specs) == 0 {
		return
	}
	ctx := b.transferContext()
	b.vmodel.Upload(ctx, bucket, specs, b.prompts())
}

func (b *browser) askReplace(ctx context.Context, key string, existing adminv1.ObjectMeta) vm.ReplaceChoice {
	choices := make(chan vm.ReplaceChoice, 1)
	b.ui(func() {
		var dlg *shell.Dialog
		pick := func(choice vm.ReplaceChoice) func() {
			return func() {
				dlg.Hide()
				choices <- choice
			}
		}
		skip := widget.NewButton("Skip", pick(vm.SkipOnce))
		replace := widget.NewButton("Replace", pick(vm.ReplaceOnce))
		replace.Importance = widget.DangerImportance
		skipAll := widget.NewButton("Skip all", pick(vm.SkipAll))
		replaceAll := widget.NewButton("Replace all", pick(vm.ReplaceAll))
		replaceAll.Importance = widget.DangerImportance
		body := widget.NewLabel(key + " already exists (" + shell.FormatBytes(existing.SizeBytes) + "). Replace it with the new file?")
		body.Wrapping = fyne.TextWrapWord
		dlg = newChoiceDialog(b.win, "objects/replace", "Replace existing object?", body, skip, replace, skipAll, replaceAll)
		dlg.Show()
		b.win.Canvas().Focus(skip)
	})
	select {
	case choice := <-choices:
		return choice
	case <-ctx.Done():
		return vm.SkipOnce
	}
}

func (b *browser) askConflict(ctx context.Context, path string) vm.ConflictChoice {
	choices := make(chan vm.ConflictChoice, 1)
	b.ui(func() {
		var dlg *shell.Dialog
		pick := func(choice vm.ConflictChoice) func() {
			return func() {
				dlg.Hide()
				choices <- choice
			}
		}
		skip := widget.NewButton("Skip", pick(vm.ConflictSkip))
		keep := widget.NewButton("Keep both", pick(vm.ConflictKeepBoth))
		replace := widget.NewButton("Replace", pick(vm.ConflictReplace))
		replace.Importance = widget.DangerImportance
		body := widget.NewLabel(path + " already exists.")
		body.Wrapping = fyne.TextWrapWord
		dlg = newChoiceDialog(b.win, "objects/file-exists", "File exists", body, skip, keep, replace)
		dlg.Show()
		b.win.Canvas().Focus(keep)
	})
	select {
	case choice := <-choices:
		return choice
	case <-ctx.Done():
		return vm.ConflictSkip
	}
}
