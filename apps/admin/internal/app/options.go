package app

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/embedded"
)

const (
	AppID       = "io.github.prieyan.butterdb.admin"
	windowTitle = "butterDB Admin"
)

var ErrMissingDependency = errors.New("missing dependency")

type Engine interface {
	http.RoundTripper
	embedded.Projects
}

type Options struct {
	Version     string
	Now         func() time.Time
	Engine      Engine
	Random      io.Reader
	DownloadDir string
}

func (o Options) validate() error {
	checks := []struct {
		name    string
		missing bool
	}{
		{"clock", o.Now == nil},
		{"engine", o.Engine == nil},
	}
	for _, c := range checks {
		if c.missing {
			return fmt.Errorf("app: %w: %s", ErrMissingDependency, c.name)
		}
	}
	return nil
}
