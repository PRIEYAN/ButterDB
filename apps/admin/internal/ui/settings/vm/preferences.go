package vm

import "strings"

const (
	PrefTheme                 = "theme"
	PrefDownloadDir           = "download_dir"
	PrefUploadConcurrency     = "upload_concurrency"
	PrefClipboardClearSeconds = "clipboard_clear_seconds"
	prefLastRoutePrefix       = "last_route."

	ThemeSystem = "system"
	ThemeLight  = "light"
	ThemeDark   = "dark"

	DefaultUploadConcurrency = 2
	MinUploadConcurrency     = 1
	MaxUploadConcurrency     = 4
	DefaultClipboardClear    = 60
)

type PreferencesState struct {
	Theme                 string
	DownloadDir           string
	UploadConcurrency     int
	ClipboardClearSeconds int
}

func LastRouteKey(serverID string) string {
	return prefLastRoutePrefix + serverID
}

func ThemeModes() []string {
	return []string{ThemeSystem, ThemeLight, ThemeDark}
}

func (v *ViewModel) readPreferences() PreferencesState {
	s := v.snapshot()
	defaults := PreferencesState{
		Theme:                 ThemeSystem,
		DownloadDir:           s.DefaultDownloadDir,
		UploadConcurrency:     DefaultUploadConcurrency,
		ClipboardClearSeconds: DefaultClipboardClear,
	}
	if s.Preferences == nil {
		return defaults
	}
	p := s.Preferences
	read := PreferencesState{
		Theme:                 p.StringWithFallback(PrefTheme, defaults.Theme),
		DownloadDir:           p.StringWithFallback(PrefDownloadDir, defaults.DownloadDir),
		UploadConcurrency:     p.IntWithFallback(PrefUploadConcurrency, defaults.UploadConcurrency),
		ClipboardClearSeconds: p.IntWithFallback(PrefClipboardClearSeconds, defaults.ClipboardClearSeconds),
	}
	if !validTheme(read.Theme) {
		read.Theme = ThemeSystem
	}
	read.UploadConcurrency = min(max(read.UploadConcurrency, MinUploadConcurrency), MaxUploadConcurrency)
	read.ClipboardClearSeconds = max(read.ClipboardClearSeconds, 0)
	return read
}

func validTheme(mode string) bool {
	for _, m := range ThemeModes() {
		if m == mode {
			return true
		}
	}
	return false
}

func (v *ViewModel) SetTheme(mode string) bool {
	v.begin()
	if !validTheme(mode) {
		v.fail(inlineProblem(fieldTheme, textThemeUnknown))
		return false
	}
	return v.savePreference(func(p Preferences) { p.SetString(PrefTheme, mode) })
}

func (v *ViewModel) SetDownloadDir(dir string) bool {
	v.begin()
	dir = strings.TrimSpace(dir)
	if dir == "" {
		v.fail(inlineProblem(fieldDownloadDir, textDownloadDirEmpty))
		return false
	}
	return v.savePreference(func(p Preferences) { p.SetString(PrefDownloadDir, dir) })
}

func (v *ViewModel) SetUploadConcurrency(n int) bool {
	v.begin()
	if n < MinUploadConcurrency || n > MaxUploadConcurrency {
		v.fail(inlineProblem(fieldConcurrency, textConcurrency))
		return false
	}
	return v.savePreference(func(p Preferences) { p.SetInt(PrefUploadConcurrency, n) })
}

func (v *ViewModel) SetClipboardClearSeconds(n int) bool {
	v.begin()
	if n < 0 {
		v.fail(inlineProblem(fieldClipboard, textClipboardSeconds))
		return false
	}
	return v.savePreference(func(p Preferences) { p.SetInt(PrefClipboardClearSeconds, n) })
}

func (v *ViewModel) savePreference(write func(Preferences)) bool {
	p := v.snapshot().Preferences
	if p == nil {
		v.fail(dialogProblem(textNotAvailable))
		return false
	}
	write(p)
	v.finish(func(s *State) { s.Preferences = v.readPreferences() }, nil)
	return true
}
