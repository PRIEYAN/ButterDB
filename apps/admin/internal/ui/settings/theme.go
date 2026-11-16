package settings

import (
	"fyne.io/fyne/v2"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/settings/vm"
)

func ThemeFor(mode string) fyne.Theme {
	switch mode {
	case vm.ThemeLight:
		return shell.NewParchmentTheme()
	case vm.ThemeDark:
		return shell.NewTerminalTheme()
	}
	return shell.NewSystemTheme()
}
