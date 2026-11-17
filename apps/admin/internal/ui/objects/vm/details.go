package vm

import "context"

func (v *ViewModel) Focus(ctx context.Context, key string) {
	v.mu.Lock()
	bucket := v.bucket
	v.focusGen++
	focus := v.focusGen
	v.mu.Unlock()
	if bucket == "" || key == "" {
		return
	}
	v.setFocus(focus, func() {
		v.details = Details{Key: key, Loading: true}
	})
	meta, err := v.client.ObjectMeta(ctx, bucket, key)
	if err != nil {
		v.setFocus(focus, func() { v.details = Details{Key: key, Problem: v.problemFor(err)} })
		return
	}
	v.setFocus(focus, func() { v.details = Details{Key: key, Meta: &meta} })
}

func (v *ViewModel) ClearFocus() {
	v.mutate(func() {
		v.focusGen++
		v.details = Details{}
	})
}

func (v *ViewModel) setFocus(focus int, fn func()) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.focusGen != focus {
		return false
	}
	fn()
	v.state.Set(v.snapshotLocked())
	return true
}
