package telegramlink

import (
	"context"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

const screenTitle = "Link Telegram"

type Screen struct {
	wizard *LoginWizard
}

func NewScreen(wizard *LoginWizard) *Screen {
	return &Screen{wizard: wizard}
}

func (s *Screen) Route() model.Route         { return model.RouteTelegram }
func (s *Screen) Title() string              { return screenTitle }
func (s *Screen) Icon() fyne.Resource        { return theme.AccountIcon() }
func (s *Screen) Shortcut() fyne.KeyName     { return "" }
func (s *Screen) Content() fyne.CanvasObject { return s.wizard.Content() }

func (s *Screen) Show(ctx context.Context, _ model.Params) {
	s.wizard.Start(ctx)
}

func (s *Screen) Hide() {
	s.wizard.ClearSecrets()
}

func (s *Screen) OnLinked(fn func(adminv1.TelegramAccount)) {
	s.wizard.OnLinked(fn)
}
