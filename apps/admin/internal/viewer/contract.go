package viewer

import (
	"context"
	"io"

	"fyne.io/fyne/v2"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
)

type Source interface {
	ObjectMeta(context.Context, string, string) (adminv1.ObjectMeta, error)
	GetObject(context.Context, string, string, adminv1.GetObjectOptions) (adminv1.ObjectStream, error)
}

type Player interface {
	Play(context.Context, uintptr, string) error
}

type WindowFactory interface {
	NewWindow(string) fyne.Window
}

type Dependencies struct {
	Windows WindowFactory
	Source  Source
	Player  Player
	Random  io.Reader
}
