package settings

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/settings/vm"
)

type forcedVariantTheme struct {
	base    fyne.Theme
	variant fyne.ThemeVariant
}

func (t forcedVariantTheme) Color(name fyne.ThemeColorName, _ fyne.ThemeVariant) color.Color {
	return t.base.Color(name, t.variant)
}

func (t forcedVariantTheme) Font(style fyne.TextStyle) fyne.Resource { return t.base.Font(style) }

func (t forcedVariantTheme) Icon(name fyne.ThemeIconName) fyne.Resource { return t.base.Icon(name) }

func (t forcedVariantTheme) Size(name fyne.ThemeSizeName) float32 { return t.base.Size(name) }

func ThemeFor(mode string) fyne.Theme {
	switch mode {
	case vm.ThemeLight:
		return forcedVariantTheme{base: theme.DefaultTheme(), variant: theme.VariantLight}
	case vm.ThemeDark:
		return forcedVariantTheme{base: theme.DefaultTheme(), variant: theme.VariantDark}
	}
	return theme.DefaultTheme()
}
