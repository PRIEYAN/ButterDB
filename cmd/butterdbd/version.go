package main

import (
	"encoding/json"
	"fmt"
	"runtime"
)

type versionReport struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	GoVersion string `json:"go_version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

func (a *application) runVersion(args []string) int {
	fs, _ := a.newFlagSet("version")
	asJSON := fs.Bool("json", false, "print one JSON object")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	report := versionReport{
		Version:   a.build.Version,
		Commit:    a.build.Commit,
		GoVersion: runtime.Version(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
	}
	if *asJSON {
		if err := json.NewEncoder(a.stdout).Encode(report); err != nil {
			a.fail(err)
			return exitFailure
		}
		return exitOK
	}
	fmt.Fprintf(a.stdout, "%s %s (%s, %s, %s/%s)\n", programName, report.Version, report.Commit, report.GoVersion, report.OS, report.Arch)
	return exitOK
}
