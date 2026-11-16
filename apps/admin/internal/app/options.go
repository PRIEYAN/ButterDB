package app

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	connectvm "github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/connect/vm"
)

const (
	AppID       = "io.github.prieyan.butterdb.admin"
	windowTitle = "butterDB Admin"
)

var ErrMissingDependency = errors.New("missing dependency")

type Options struct {
	Version     string
	Now         func() time.Time
	Dialer      connectvm.Dialer
	Tokens      model.TokenStore
	Store       model.ProfileStore
	Files       model.TokenStore
	Memory      model.TokenStore
	Random      io.Reader
	DeviceName  string
	DownloadDir string
}

func (o Options) validate() error {
	checks := []struct {
		name    string
		missing bool
	}{
		{"clock", o.Now == nil},
		{"dialer", o.Dialer == nil},
		{"keychain tokens", o.Tokens == nil},
		{"file tokens", o.Files == nil},
		{"memory tokens", o.Memory == nil},
		{"profile store", o.Store == nil},
	}
	for _, c := range checks {
		if c.missing {
			return fmt.Errorf("app: %w: %s", ErrMissingDependency, c.name)
		}
	}
	return nil
}

func (o Options) tokenStoreFor(kind model.TokenStorage) model.TokenStore {
	switch kind {
	case model.TokenFile:
		return o.Files
	case model.TokenMemory:
		return o.Memory
	default:
		return o.Tokens
	}
}
