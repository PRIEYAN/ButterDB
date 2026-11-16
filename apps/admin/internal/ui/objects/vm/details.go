package vm

import (
	"context"
	"fmt"
	"io"
	"mime"
	"slices"
	"strings"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
)

const (
	PreviewMaxBytes  = 20 * 1024 * 1024
	PreviewMaxPixels = 40_000_000
)

func PreviewTypes() []string {
	return []string{"image/jpeg", "image/png", "image/gif", "image/webp", "image/bmp"}
}

func PreviewSupported(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	}
	return slices.Contains(PreviewTypes(), strings.ToLower(mediaType))
}

func ExceedsPixelCeiling(width, height int) bool {
	return width <= 0 || height <= 0 || int64(width)*int64(height) > PreviewMaxPixels
}

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
		v.preview = Preview{Key: key, Status: PreviewNone}
	})
	meta, err := v.client.ObjectMeta(ctx, bucket, key)
	if err != nil {
		v.setFocus(focus, func() { v.details = Details{Key: key, Problem: v.problemFor(err)} })
		return
	}
	v.setFocus(focus, func() { v.details = Details{Key: key, Meta: &meta} })
	v.loadPreview(ctx, focus, bucket, meta)
}

func (v *ViewModel) ClearFocus() {
	v.mutate(func() {
		v.focusGen++
		v.details = Details{}
		v.preview = Preview{}
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

func (v *ViewModel) loadPreview(ctx context.Context, focus int, bucket string, meta adminv1.ObjectMeta) {
	if !PreviewSupported(meta.ContentType) {
		v.setFocus(focus, func() {
			v.preview = Preview{Key: meta.Key, Status: PreviewUnsupported, ContentType: meta.ContentType, Message: "No preview for " + meta.ContentType}
		})
		return
	}
	if meta.SizeBytes > PreviewMaxBytes {
		v.setFocus(focus, func() { v.preview = tooLargePreview(meta) })
		return
	}
	v.setFocus(focus, func() { v.preview = Preview{Key: meta.Key, Status: PreviewLoading, ContentType: meta.ContentType} })
	stream, err := v.client.GetObject(ctx, bucket, meta.Key, adminv1.GetObjectOptions{})
	if err != nil {
		v.setFocus(focus, func() {
			v.preview = Preview{Key: meta.Key, Status: PreviewFailed, ContentType: meta.ContentType, Problem: v.problemFor(err)}
		})
		return
	}
	defer stream.Body.Close()
	if stream.ContentLength > PreviewMaxBytes {
		v.setFocus(focus, func() { v.preview = tooLargePreview(meta) })
		return
	}
	data, err := io.ReadAll(io.LimitReader(stream.Body, PreviewMaxBytes+1))
	switch {
	case err != nil:
		v.setFocus(focus, func() {
			v.preview = Preview{Key: meta.Key, Status: PreviewFailed, ContentType: meta.ContentType, Problem: v.problemFor(err)}
		})
	case int64(len(data)) > PreviewMaxBytes:
		v.setFocus(focus, func() { v.preview = tooLargePreview(meta) })
	default:
		v.setFocus(focus, func() {
			v.preview = Preview{Key: meta.Key, Status: PreviewReady, ContentType: meta.ContentType, Data: data}
		})
	}
}

func tooLargePreview(meta adminv1.ObjectMeta) Preview {
	return Preview{
		Key:         meta.Key,
		Status:      PreviewTooLarge,
		ContentType: meta.ContentType,
		Message:     fmt.Sprintf("Too large to preview (limit %d MiB)", PreviewMaxBytes/(1024*1024)),
	}
}

func (v *ViewModel) MarkPreviewFailed(key string, reason string) {
	v.mutate(func() {
		if v.preview.Key != key {
			return
		}
		v.preview = Preview{Key: key, Status: PreviewUnsupported, ContentType: v.preview.ContentType, Message: reason}
	})
}
