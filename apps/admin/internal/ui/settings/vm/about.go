package vm

import (
	"runtime"
	"runtime/debug"
)

const (
	fyneModule     = "fyne.io/fyne/v2"
	versionUnknown = "unknown"
)

type Licence struct {
	Name    string
	Licence string
}

type AboutState struct {
	AppVersion    string
	GoVersion     string
	FyneVersion   string
	ServerVersion string
	Licences      []Licence
}

func aboutStatic(appVersion, serverVersion string) AboutState {
	if appVersion == "" {
		appVersion = versionUnknown
	}
	return AboutState{
		AppVersion:    appVersion,
		GoVersion:     runtime.Version(),
		FyneVersion:   moduleVersion(fyneModule),
		ServerVersion: serverVersion,
		Licences: []Licence{
			{Name: "Fyne", Licence: "BSD-3-Clause"},
			{Name: "golang.org/x/image", Licence: "BSD-3-Clause"},
		},
	}
}

func moduleVersion(path string) string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return versionUnknown
	}
	for _, dep := range info.Deps {
		if dep.Path == path {
			return dep.Version
		}
	}
	return versionUnknown
}
