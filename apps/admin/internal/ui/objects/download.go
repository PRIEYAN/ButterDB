package objects

import (
	"os"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/objects/vm"
)

const nothingToDownloadHint = "Select an object to download."

func (b *browser) downloadKeys() []string {
	st := b.snapshot()
	if len(st.SelectedObjects) > 0 {
		return st.SelectedObjects
	}
	if row, ok := b.focusedRow(); ok && row.Kind == vm.RowObject {
		return []string{row.Key}
	}
	return nil
}

func (b *browser) startDownload() {
	keys := b.downloadKeys()
	if len(keys) == 0 {
		b.inform(nothingToDownloadHint)
		return
	}
	st := b.snapshot()
	if len(keys) == 1 {
		b.saveSingle(st.Bucket, keys[0])
		return
	}
	b.chooseFolder(st.Bucket, st.Prefix, keys)
}

func (b *browser) startLocation() fyne.ListableURI {
	if b.opts.DownloadDir == nil {
		return nil
	}
	dir := b.opts.DownloadDir()
	if dir == "" {
		return nil
	}
	lister, err := storage.ListerForURI(storage.NewFileURI(dir))
	if err != nil {
		return nil
	}
	return lister
}

func (b *browser) saveSingle(bucket, key string) {
	dlg := dialog.NewFileSave(func(writer fyne.URIWriteCloser, err error) {
		if err != nil {
			b.present(model.Describe(err, b.now()))
			return
		}
		if writer == nil {
			return
		}
		target := writer.URI().Path()
		_ = writer.Close()
		go b.downloadOne(bucket, key, target)
	}, b.win)
	dlg.SetFileName(vm.DefaultFileName(key))
	if location := b.startLocation(); location != nil {
		dlg.SetLocation(location)
	}
	dlg.Show()
}

func (b *browser) downloadOne(bucket, key, target string) {
	ctx := b.transferContext()
	b.vmodel.Download(ctx, bucket, []vm.DownloadRequest{{Key: key, TargetPath: target}}, vm.Prompts{})
	for _, item := range b.vmodel.Transfers().Get().Items {
		if item.Kind == vm.KindDownload && item.Key == key && item.Path == target && item.State != vm.TransferDone {
			removeIfEmpty(target)
		}
	}
}

func removeIfEmpty(path string) {
	if info, err := os.Stat(path); err == nil && info.Size() == 0 {
		_ = os.Remove(path)
	}
}

func (b *browser) chooseFolder(bucket, prefix string, keys []string) {
	dlg := dialog.NewFolderOpen(func(folder fyne.ListableURI, err error) {
		if err != nil {
			b.present(model.Describe(err, b.now()))
			return
		}
		if folder == nil {
			return
		}
		requests := vm.FolderTargets(folder.Path(), prefix, keys)
		ctx := b.transferContext()
		go b.vmodel.Download(ctx, bucket, requests, b.prompts())
	}, b.win)
	if location := b.startLocation(); location != nil {
		dlg.SetLocation(location)
	}
	dlg.Show()
}
