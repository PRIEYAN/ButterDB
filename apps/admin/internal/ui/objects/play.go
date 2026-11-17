package objects

import (
	"context"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/objects/vm"
)

const (
	nothingToPlayHint = "There are no videos or songs to play here."
	playerMissingHint = "Media playback is unavailable."
)

func (b *browser) folderMedia() []adminv1.ObjectSummary {
	var items []adminv1.ObjectSummary
	for _, row := range b.snapshot().Rows {
		if row.Kind == vm.RowObject && model.MediaKindOf(row.Key, row.Object.ContentType) != model.MediaNone {
			items = append(items, row.Object)
		}
	}
	return items
}

func (b *browser) playMedia() {
	if b.opts.Player == nil {
		b.inform(playerMissingHint)
		return
	}
	st := b.snapshot()
	items := b.folderMedia()
	if len(st.SelectedObjects) > 0 {
		checked := make(map[string]bool, len(st.SelectedObjects))
		for _, key := range st.SelectedObjects {
			checked[key] = true
		}
		var selected []adminv1.ObjectSummary
		for _, item := range items {
			if checked[item.Key] {
				selected = append(selected, item)
			}
		}
		b.startPlayer(selected, 0)
		return
	}
	b.startPlayer(items, 0)
}

func (b *browser) playFrom(key string) {
	items := b.folderMedia()
	for i, item := range items {
		if item.Key == key {
			b.startPlayer(items, i)
			return
		}
	}
}

func (b *browser) startPlayer(items []adminv1.ObjectSummary, start int) {
	if len(items) == 0 {
		b.inform(nothingToPlayHint)
		return
	}
	ctx := b.opts.Context
	if ctx == nil {
		ctx = context.Background()
	}
	b.opts.Player.Play(ctx, b.snapshot().Bucket, items, start, func(message string) {
		b.ui(func() { b.inform(message) })
	})
}
