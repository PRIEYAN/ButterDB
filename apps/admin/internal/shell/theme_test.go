package shell

import (
	"image/color"
	"math"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

func channel(v uint32) float64 {
	c := float64(v) / 0xffff
	if c <= 0.03928 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

func luminance(c color.Color) float64 {
	r, g, b, _ := c.RGBA()
	return 0.2126*channel(r) + 0.7152*channel(g) + 0.0722*channel(b)
}

func contrast(a, b color.Color) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func TestThemeContrast(t *testing.T) {
	light, dark := NewParchmentTheme(), NewTerminalTheme()
	l := func(n fyne.ThemeColorName) color.Color { return light.Color(n, theme.VariantLight) }
	d := func(n fyne.ThemeColorName) color.Color { return dark.Color(n, theme.VariantDark) }
	cases := []struct {
		name string
		fg   color.Color
		bg   color.Color
		min  float64
	}{
		{name: "ink on parchment", fg: l(theme.ColorNameForeground), bg: l(theme.ColorNameBackground), min: 7},
		{name: "ink on paper", fg: l(theme.ColorNameForeground), bg: l(colorNameSurface), min: 7},
		{name: "ink on pink", fg: l(colorNameOnAccent), bg: l(colorNameAccent), min: 4.5},
		{name: "ink on pink in terminal", fg: d(colorNameOnAccent), bg: d(colorNameAccent), min: 4.5},
		{name: "secondary text on parchment", fg: l(colorNameInkSecondary), bg: l(theme.ColorNameBackground), min: 4.5},
		{name: "secondary text on paper", fg: l(colorNameInkSecondary), bg: l(colorNameSurface), min: 4.5},
		{name: "primary button label", fg: l(theme.ColorNameForegroundOnPrimary), bg: l(theme.ColorNamePrimary), min: 7},
		{name: "danger button label", fg: l(theme.ColorNameForegroundOnError), bg: l(theme.ColorNameError), min: 4.5},
		{name: "error text on parchment", fg: l(theme.ColorNameError), bg: l(theme.ColorNameBackground), min: 4.5},
		{name: "link on parchment", fg: l(theme.ColorNameHyperlink), bg: l(theme.ColorNameBackground), min: 4.5},
		{name: "ink on pink selection", fg: l(theme.ColorNameForeground), bg: l(theme.ColorNameSelection), min: 4.5},
		{name: "placeholder on input", fg: l(theme.ColorNamePlaceHolder), bg: l(theme.ColorNameInputBackground), min: 4.5},
		{name: "input border on input", fg: l(theme.ColorNameInputBorder), bg: l(theme.ColorNameInputBackground), min: 3},
		{name: "success icon on parchment", fg: l(theme.ColorNameSuccess), bg: l(theme.ColorNameBackground), min: 2.9},
		{name: "warning icon on parchment", fg: l(theme.ColorNameWarning), bg: l(theme.ColorNameBackground), min: 2.8},
		{name: "inverse on terminal", fg: d(theme.ColorNameForeground), bg: d(theme.ColorNameBackground), min: 7},
		{name: "inverse on terminal surface", fg: d(theme.ColorNameForeground), bg: d(colorNameSurface), min: 7},
		{name: "terminal secondary text", fg: d(colorNameInkSecondary), bg: d(theme.ColorNameBackground), min: 4.5},
		{name: "terminal primary button label", fg: d(theme.ColorNameForegroundOnPrimary), bg: d(theme.ColorNamePrimary), min: 7},
		{name: "terminal danger button label", fg: d(theme.ColorNameForegroundOnError), bg: d(theme.ColorNameError), min: 4.5},
		{name: "terminal link", fg: d(theme.ColorNameHyperlink), bg: d(theme.ColorNameBackground), min: 4.5},
		{name: "inverse on terminal selection", fg: d(theme.ColorNameForeground), bg: d(theme.ColorNameSelection), min: 4.5},
		{name: "terminal success icon", fg: d(theme.ColorNameSuccess), bg: d(theme.ColorNameBackground), min: 3},
		{name: "terminal warning icon", fg: d(theme.ColorNameWarning), bg: d(theme.ColorNameBackground), min: 3},
		{name: "terminal input border", fg: d(theme.ColorNameInputBorder), bg: d(theme.ColorNameInputBackground), min: 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ratio := contrast(tc.fg, tc.bg)
			t.Logf("%s: %.2f:1", tc.name, ratio)
			if ratio < tc.min {
				t.Fatalf("%s contrast %.2f:1, want at least %.1f:1", tc.name, ratio, tc.min)
			}
		})
	}
}

func TestThemeTokens(t *testing.T) {
	light, dark := NewParchmentTheme(), NewTerminalTheme()
	cases := []struct {
		name string
		th   fyne.Theme
		key  fyne.ThemeColorName
		want color.NRGBA
	}{
		{name: "parchment background", th: light, key: theme.ColorNameBackground, want: rgb(0xF7F4EB)},
		{name: "paper cards", th: light, key: colorNameSurface, want: rgb(0xFFFFFF)},
		{name: "ink text", th: light, key: theme.ColorNameForeground, want: rgb(0x121212)},
		{name: "pink selection", th: light, key: theme.ColorNameSelection, want: rgb(0xFF66C4)},
		{name: "subtle rules", th: light, key: theme.ColorNameSeparator, want: rgb(0xD4D1C7)},
		{name: "blue links", th: light, key: theme.ColorNameHyperlink, want: rgb(0x2563EB)},
		{name: "confident status", th: light, key: theme.ColorNameSuccess, want: rgb(0x16A34A)},
		{name: "uncertain status", th: light, key: theme.ColorNameWarning, want: rgb(0xD97706)},
		{name: "terminal background", th: dark, key: theme.ColorNameBackground, want: rgb(0x111214)},
		{name: "terminal text", th: dark, key: theme.ColorNameForeground, want: rgb(0xF7F4EB)},
		{name: "terminal accent", th: dark, key: colorNameAccent, want: rgb(0xFF66C4)},
		{name: "no soft shadows", th: light, key: theme.ColorNameShadow, want: color.NRGBA{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := color.NRGBAModel.Convert(tc.th.Color(tc.key, theme.VariantLight)).(color.NRGBA)
			if got != tc.want {
				t.Fatalf("%s = %v, want %v", tc.key, got, tc.want)
			}
		})
	}
}

func TestThemeFontsAndSizes(t *testing.T) {
	th := NewSystemTheme()
	cases := []struct {
		name  string
		style fyne.TextStyle
		want  string
	}{
		{name: "body text uses Inter", style: fyne.TextStyle{}, want: "Inter-Regular.ttf"},
		{name: "headings and buttons use Space Mono", style: fyne.TextStyle{Bold: true}, want: "SpaceMono-Bold.ttf"},
		{name: "data uses JetBrains Mono", style: fyne.TextStyle{Monospace: true}, want: "JetBrainsMonoNL-Regular.ttf"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			font := th.Font(tc.style)
			if font == nil || font.Name() != tc.want || len(font.Content()) == 0 {
				t.Fatalf("font for %+v = %v, want %s", tc.style, font, tc.want)
			}
		})
	}
	if got := th.Size(theme.SizeNameInputRadius); got != radiusSmall {
		t.Fatalf("input radius = %v, want %v", got, radiusSmall)
	}
	if got := th.Size(theme.SizeNamePadding); got != theme.DefaultTheme().Size(theme.SizeNamePadding) {
		t.Fatalf("padding = %v, want Fyne's %v", got, theme.DefaultTheme().Size(theme.SizeNamePadding))
	}
}

func TestSystemThemeFollowsVariant(t *testing.T) {
	th := NewSystemTheme()
	lightBG := color.NRGBAModel.Convert(th.Color(theme.ColorNameBackground, theme.VariantLight))
	darkBG := color.NRGBAModel.Convert(th.Color(theme.ColorNameBackground, theme.VariantDark))
	if lightBG != rgb(0xF7F4EB) || darkBG != rgb(0x111214) {
		t.Fatalf("system theme backgrounds = %v and %v", lightBG, darkBG)
	}
}
