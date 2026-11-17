package viewer

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"mime"
	"path"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
)

var ErrPlayerUnavailable = errors.New("MPV is not installed")

type Viewer struct {
	deps Dependencies
	post func(func())
}

type fileWindow struct {
	viewer   *Viewer
	win      fyne.Window
	ctx      context.Context
	cancel   context.CancelFunc
	progress *widget.ProgressBarInfinite
	closed   bool
}

func New(deps Dependencies) (*Viewer, error) {
	switch {
	case deps.Windows == nil:
		return nil, fmt.Errorf("viewer: missing window factory")
	case deps.Source == nil:
		return nil, fmt.Errorf("viewer: missing object source")
	case deps.Player == nil:
		return nil, fmt.Errorf("viewer: missing media player")
	case deps.Random == nil:
		return nil, fmt.Errorf("viewer: missing random source")
	}
	return &Viewer{deps: deps, post: fyne.Do}, nil
}

func (v *Viewer) Open(parent context.Context, bucket string, object adminv1.ObjectSummary) {
	if parent.Err() != nil {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	s := &fileWindow{viewer: v, win: v.deps.Windows.NewWindow(path.Base(object.Key) + " — butterDB"), ctx: ctx, cancel: cancel}
	s.progress = widget.NewProgressBarInfinite()
	s.win.SetContent(container.NewCenter(container.NewVBox(widget.NewLabel("Opening file…"), s.progress)))
	s.win.Resize(fyne.NewSize(960, 640))
	s.win.SetOnClosed(func() {
		s.closed = true
		s.progress.Stop()
		cancel()
	})
	context.AfterFunc(ctx, func() {
		v.post(func() {
			if !s.closed {
				s.win.Close()
			}
		})
	})
	s.win.Show()
	go s.load(bucket, object)
}

func (s *fileWindow) update(fn func()) {
	s.viewer.post(func() {
		if s.ctx.Err() != nil || s.closed {
			return
		}
		s.progress.Stop()
		fn()
	})
}

func (s *fileWindow) load(bucket string, object adminv1.ObjectSummary) {
	meta, err := s.viewer.deps.Source.ObjectMeta(s.ctx, bucket, object.Key)
	if err != nil {
		s.fail("The file could not be loaded. It may have been deleted or the connection may be unavailable.")
		return
	}
	meta.Key = object.Key
	switch fileKind(meta.ContentType, object.Key) {
	case "image":
		s.loadImage(bucket, meta)
	case "media":
		s.loadMedia(bucket, meta)
	default:
		s.update(func() { s.win.SetContent(fileCard(meta)) })
	}
}

func (s *fileWindow) fail(message string) {
	s.update(func() {
		body := widget.NewLabel(message)
		body.Wrapping = fyne.TextWrapWord
		s.win.SetContent(shell.PageMargins(container.NewCenter(container.NewVBox(shell.NewHeading("Cannot open file"), body))))
	})
}

func fileCard(meta adminv1.ObjectMeta) fyne.CanvasObject {
	icon := canvas.NewImageFromResource(theme.FileIcon())
	icon.FillMode = canvas.ImageFillContain
	icon.SetMinSize(fyne.NewSize(96, 96))
	name := shell.NewHeading(path.Base(meta.Key))
	name.Alignment = fyne.TextAlignCenter
	name.Wrapping = fyne.TextWrapWord
	info := widget.NewLabel(shell.FormatBytes(meta.SizeBytes) + " · " + meta.ContentType)
	info.Alignment = fyne.TextAlignCenter
	info.Wrapping = fyne.TextWrapWord
	return shell.PageMargins(container.NewCenter(container.NewVBox(container.NewCenter(icon), name, info)))
}

func fileKind(contentType, key string) string {
	contentType, _, _ = mime.ParseMediaType(strings.ToLower(contentType))
	if contentType == "" || contentType == "application/octet-stream" {
		contentType = mime.TypeByExtension(strings.ToLower(path.Ext(key)))
	}
	if strings.HasPrefix(contentType, "image/") && contentType != "image/svg+xml" {
		return "image"
	}
	if strings.HasPrefix(contentType, "video/") || strings.HasPrefix(contentType, "audio/") || contentType == "application/ogg" {
		return "media"
	}
	return "file"
}

func nativeHandle(win fyne.Window) uintptr {
	var handle uintptr
	if native, ok := win.(driver.NativeWindow); ok {
		native.RunNative(func(ctx any) {
			switch ctx := ctx.(type) {
			case driver.X11WindowContext:
				handle = ctx.WindowHandle
			case driver.WindowsWindowContext:
				handle = ctx.HWND
			}
		})
	}
	return handle
}

func (s *fileWindow) loadMedia(bucket string, meta adminv1.ObjectMeta) {
	stream, err := newStream(s.ctx, s.viewer.deps.Source, bucket, meta, s.viewer.deps.Random)
	if err != nil {
		s.fail("The playback stream could not be started.")
		return
	}
	s.update(func() {
		handle := nativeHandle(s.win)
		if handle == 0 {
			_ = stream.Close()
			s.fail("Native playback requires an X11/XWayland or Windows build of butterDB.")
			return
		}
		s.win.SetContent(canvas.NewRectangle(color.Black))
		go func() {
			defer stream.Close()
			err := s.viewer.deps.Player.Play(s.ctx, handle, stream.URL)
			if s.ctx.Err() != nil {
				return
			}
			if errors.Is(err, ErrPlayerUnavailable) {
				s.fail("Install MPV to play videos and music, then open this file again.")
			} else if err != nil {
				s.fail("MPV could not play this file. The file may be unavailable or use an unsupported format.")
			} else {
				s.update(func() { s.win.Close() })
			}
		}()
	})
}
