package shell

import (
	"context"

	"fyne.io/fyne/v2"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

type Screen interface {
	Route() model.Route
	Title() string
	Icon() fyne.Resource
	Shortcut() fyne.KeyName
	Content() fyne.CanvasObject
	Show(ctx context.Context, p model.Params)
	Hide()
}

type Navigator interface {
	Navigate(r model.Route, p model.Params)
	Current() model.Route
	SetEnabled(r model.Route, enabled bool, reason string)
}

type Notifier interface {
	Problem(p model.Problem)
	Info(text string)
	ClearBanner()
}
