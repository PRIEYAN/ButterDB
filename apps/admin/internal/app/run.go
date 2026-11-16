package app

import (
	"fmt"

	fyneapp "fyne.io/fyne/v2/app"
)

func Run(opts Options) error {
	if err := opts.validate(); err != nil {
		return err
	}
	a := fyneapp.NewWithID(AppID)
	w := a.NewWindow(windowTitle)
	w.SetMaster()
	if _, err := Build(a, w, opts); err != nil {
		return fmt.Errorf("app: build: %w", err)
	}
	w.ShowAndRun()
	return nil
}
