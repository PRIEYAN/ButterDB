package shell

import (
	"embed"
	"image/color"
	"path"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

//go:embed fonts/Inter-Regular.ttf fonts/SpaceMono-Bold.ttf fonts/JetBrainsMonoNL-Regular.ttf
var fontFiles embed.FS

const (
	fontBodyFile    = "fonts/Inter-Regular.ttf"
	fontDisplayFile = "fonts/SpaceMono-Bold.ttf"
	fontMonoFile    = "fonts/JetBrainsMonoNL-Regular.ttf"
)

const (
	colorNameSurface      fyne.ThemeColorName = "butterdb.surface"
	colorNameBorderStrong fyne.ThemeColorName = "butterdb.borderStrong"
	colorNameSolidShadow  fyne.ThemeColorName = "butterdb.solidShadow"
	colorNameAccent       fyne.ThemeColorName = "butterdb.accent"
	colorNameOnAccent     fyne.ThemeColorName = "butterdb.onAccent"
	colorNameTitleBar     fyne.ThemeColorName = "butterdb.titleBar"
	colorNameInfo         fyne.ThemeColorName = "butterdb.info"
	colorNameInkSecondary fyne.ThemeColorName = "butterdb.inkSecondary"
)

const (
	gridUnit       float32 = 8
	cardPadding    float32 = 3 * gridUnit
	contentMargin  float32 = 3 * gridUnit
	titleBarHeight float32 = 28
	shadowOffset   float32 = 4
	borderWidth    float32 = 1
	radiusSmall    float32 = 2
	glyphSize      float32 = 9
)

type fontSet struct {
	body    fyne.Resource
	display fyne.Resource
	mono    fyne.Resource
}

func loadFonts() fontSet {
	return fontSet{
		body:    embeddedFont(fontBodyFile),
		display: embeddedFont(fontDisplayFile),
		mono:    embeddedFont(fontMonoFile),
	}
}

func embeddedFont(name string) fyne.Resource {
	data, err := fontFiles.ReadFile(name)
	if err != nil {
		return nil
	}
	return fyne.NewStaticResource(path.Base(name), data)
}

type designTheme struct {
	fonts   fontSet
	variant fyne.ThemeVariant
	forced  bool
}

func NewSystemTheme() fyne.Theme {
	return &designTheme{fonts: loadFonts()}
}

func NewParchmentTheme() fyne.Theme {
	return &designTheme{fonts: loadFonts(), variant: theme.VariantLight, forced: true}
}

func NewTerminalTheme() fyne.Theme {
	return &designTheme{fonts: loadFonts(), variant: theme.VariantDark, forced: true}
}

func (t *designTheme) effectiveVariant(v fyne.ThemeVariant) fyne.ThemeVariant {
	if t.forced {
		return t.variant
	}
	return v
}

func (t *designTheme) Color(name fyne.ThemeColorName, v fyne.ThemeVariant) color.Color {
	v = t.effectiveVariant(v)
	if c, ok := paletteColor(name, v); ok {
		return c
	}
	return theme.DefaultTheme().Color(name, v)
}

func (t *designTheme) Font(style fyne.TextStyle) fyne.Resource {
	var chosen fyne.Resource
	switch {
	case style.Symbol:
		return theme.DefaultTheme().Font(style)
	case style.Monospace:
		chosen = t.fonts.mono
	case style.Bold:
		chosen = t.fonts.display
	default:
		chosen = t.fonts.body
	}
	if chosen == nil {
		return theme.DefaultTheme().Font(style)
	}
	return chosen
}

func (t *designTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return theme.DefaultTheme().Icon(name)
}

func (t *designTheme) Size(name fyne.ThemeSizeName) float32 {
	switch name {
	case theme.SizeNameInnerPadding:
		return 10
	case theme.SizeNameHeadingText:
		return 24
	case theme.SizeNameSubHeadingText:
		return 17
	case theme.SizeNameCaptionText:
		return 12
	case theme.SizeNameInputRadius:
		return radiusSmall
	case theme.SizeNameSelectionRadius, theme.SizeNameScrollBarRadius:
		return 0
	case theme.SizeNameInputBorder, theme.SizeNameSeparatorThickness:
		return borderWidth
	}
	return theme.DefaultTheme().Size(name)
}

func paletteColor(name fyne.ThemeColorName, v fyne.ThemeVariant) (color.Color, bool) {
	if c, ok := sharedColor(name); ok {
		return c, true
	}
	if v == theme.VariantDark {
		return terminalColor(name)
	}
	return parchmentColor(name)
}

var _ fyne.Theme = (*designTheme)(nil)

func rgb(hex uint32) color.NRGBA {
	return color.NRGBA{R: uint8(hex >> 16), G: uint8(hex >> 8), B: uint8(hex), A: 0xff}
}

func rgba(hex uint32, alpha uint8) color.NRGBA {
	c := rgb(hex)
	c.A = alpha
	return c
}

const (
	inkPrimary       = 0x121212
	inkSecondary     = 0x52525B
	surfaceParchment = 0xF7F4EB
	surfacePaper     = 0xFFFFFF
	surfaceTerminal  = 0x111214
	terminalRaised   = 0x1A1B1F
	accentPink       = 0xFF66C4
	accentPinkSoft   = 0xFFB3E1
	accentPinkDeep   = 0x7C3863
	accentBlue       = 0x2563EB
	accentBlueLight  = 0x60A5FA
	borderSubtle     = 0xD4D1C7
	statusConfident  = 0x16A34A
	statusUncertain  = 0xD97706
	errorRed         = 0xB91C1C
	errorRedLight    = 0xF87171
)

func sharedColor(name fyne.ThemeColorName) (color.Color, bool) {
	switch name {
	case theme.ColorNameShadow:
		return color.Transparent, true
	case theme.ColorNameScrollBarBackground:
		return color.Transparent, true
	case colorNameAccent:
		return rgb(accentPink), true
	case colorNameOnAccent, theme.ColorNameForegroundOnSuccess, theme.ColorNameForegroundOnWarning:
		return rgb(inkPrimary), true
	case theme.ColorNameSuccess:
		return rgb(statusConfident), true
	case theme.ColorNameWarning:
		return rgb(statusUncertain), true
	}
	return nil, false
}

func parchmentColor(name fyne.ThemeColorName) (color.Color, bool) {
	switch name {
	case theme.ColorNameBackground, theme.ColorNameOverlayBackground:
		return rgb(surfaceParchment), true
	case theme.ColorNameButton, theme.ColorNameInputBackground, theme.ColorNameMenuBackground, colorNameSurface:
		return rgb(surfacePaper), true
	case theme.ColorNameForeground, colorNameBorderStrong, colorNameSolidShadow:
		return rgb(inkPrimary), true
	case colorNameInkSecondary:
		return rgb(inkSecondary), true
	case theme.ColorNamePrimary:
		return rgb(inkPrimary), true
	case theme.ColorNameForegroundOnError, theme.ColorNameForegroundOnPrimary:
		return rgb(surfaceParchment), true
	case theme.ColorNameError:
		return rgb(errorRed), true
	case theme.ColorNameHyperlink, colorNameInfo:
		return rgb(accentBlue), true
	case theme.ColorNameSelection:
		return rgb(accentPink), true
	case theme.ColorNameHover:
		return rgba(accentPink, 0x80), true
	case theme.ColorNameFocus:
		return rgba(accentPink, 0x99), true
	case theme.ColorNamePressed:
		return rgba(accentPink, 0x66), true
	case theme.ColorNameSeparator:
		return rgb(borderSubtle), true
	case theme.ColorNameInputBorder:
		return rgb(0x8C887D), true
	case theme.ColorNameHeaderBackground, colorNameTitleBar:
		return rgb(0xEFEBE0), true
	case theme.ColorNameDisabledButton:
		return rgb(0xE6E2D7), true
	case theme.ColorNameDisabled:
		return rgb(0x8A877E), true
	case theme.ColorNamePlaceHolder:
		return rgb(0x71717A), true
	case theme.ColorNameScrollBar:
		return rgb(0xA19D92), true
	}
	return nil, false
}

func terminalColor(name fyne.ThemeColorName) (color.Color, bool) {
	switch name {
	case theme.ColorNameBackground, theme.ColorNameOverlayBackground:
		return rgb(surfaceTerminal), true
	case theme.ColorNameButton, theme.ColorNameInputBackground, theme.ColorNameMenuBackground, colorNameSurface:
		return rgb(terminalRaised), true
	case theme.ColorNameForeground, colorNameBorderStrong:
		return rgb(surfaceParchment), true
	case colorNameSolidShadow:
		return rgb(accentPink), true
	case colorNameInkSecondary:
		return rgb(0xA1A1AA), true
	case theme.ColorNamePrimary:
		return rgb(surfaceParchment), true
	case theme.ColorNameForegroundOnError, theme.ColorNameForegroundOnPrimary:
		return rgb(inkPrimary), true
	case theme.ColorNameError:
		return rgb(errorRedLight), true
	case theme.ColorNameHyperlink, colorNameInfo:
		return rgb(accentBlueLight), true
	case theme.ColorNameSelection:
		return rgb(accentPinkDeep), true
	case theme.ColorNameHover:
		return rgba(accentPink, 0x55), true
	case theme.ColorNameFocus:
		return rgba(accentPink, 0x80), true
	case theme.ColorNamePressed:
		return rgba(accentPink, 0x66), true
	case theme.ColorNameSeparator:
		return rgb(0x2E2F33), true
	case theme.ColorNameInputBorder:
		return rgb(0x6B6C72), true
	case theme.ColorNameHeaderBackground, colorNameTitleBar:
		return rgb(0x1E1F24), true
	case theme.ColorNameDisabledButton:
		return rgb(0x26272B), true
	case theme.ColorNameDisabled:
		return rgb(0x6B6B70), true
	case theme.ColorNamePlaceHolder:
		return rgb(0x9A9AA0), true
	case theme.ColorNameScrollBar:
		return rgb(0x5A5B60), true
	}
	return nil, false
}

func designColor(th fyne.Theme, name fyne.ThemeColorName, v fyne.ThemeVariant) color.Color {
	if _, ours := theme.Current().(*designTheme); ours {
		return th.Color(name, v)
	}
	return th.Color(fallbackColorName(name), v)
}

func fallbackColorName(name fyne.ThemeColorName) fyne.ThemeColorName {
	switch name {
	case colorNameSurface:
		return theme.ColorNameInputBackground
	case colorNameBorderStrong, colorNameSolidShadow, colorNameOnAccent:
		return theme.ColorNameForeground
	case colorNameAccent:
		return theme.ColorNameSelection
	case colorNameTitleBar:
		return theme.ColorNameHeaderBackground
	case colorNameInfo:
		return theme.ColorNameHyperlink
	case colorNameInkSecondary:
		return theme.ColorNamePlaceHolder
	}
	return name
}
