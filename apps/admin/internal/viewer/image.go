package viewer

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"

	"fyne.io/fyne/v2/canvas"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
)

const (
	imageMaxBytes  = 20 * 1024 * 1024
	imageMaxPixels = 40_000_000
)

func (s *fileWindow) loadImage(bucket string, meta adminv1.ObjectMeta) {
	img, err := readImage(s.ctx, s.viewer.deps.Source, bucket, meta)
	if err != nil {
		s.fail("This image cannot be displayed. Images must be valid and no larger than 20 MiB or 40 megapixels.")
		return
	}
	s.update(func() {
		view := canvas.NewImageFromImage(img)
		view.FillMode = canvas.ImageFillContain
		view.ScaleMode = canvas.ImageScaleSmooth
		s.win.SetContent(view)
	})
}

func readImage(ctx context.Context, source Source, bucket string, meta adminv1.ObjectMeta) (image.Image, error) {
	if meta.SizeBytes > imageMaxBytes {
		return nil, fmt.Errorf("viewer: image exceeds size limit")
	}
	object, err := source.GetObject(ctx, bucket, meta.Key, adminv1.GetObjectOptions{IfMatch: meta.ETag})
	if err != nil {
		return nil, err
	}
	defer object.Body.Close()
	stop := context.AfterFunc(ctx, func() { _ = object.Body.Close() })
	defer stop()
	if object.ContentLength > imageMaxBytes {
		return nil, fmt.Errorf("viewer: image exceeds size limit")
	}
	data, err := io.ReadAll(io.LimitReader(object.Body, imageMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > imageMaxBytes {
		return nil, fmt.Errorf("viewer: image exceeds size limit")
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > imageMaxPixels/config.Height {
		return nil, fmt.Errorf("viewer: image exceeds pixel limit")
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	return img, err
}
