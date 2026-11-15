package multipart

import (
	"errors"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
)

func validDependencies(t *testing.T) Dependencies {
	t.Helper()
	fx := newFixture(t, fixtureOptions{})
	return fx.handlers.deps
}

func TestNewHandlersRejectsMissingDependencies(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Dependencies)
	}{
		{name: "stage", mutate: func(d *Dependencies) { d.Stage = nil }},
		{name: "verifier", mutate: func(d *Dependencies) { d.Verifier = nil }},
		{name: "clock", mutate: func(d *Dependencies) { d.Clock = nil }},
		{name: "authorizer", mutate: func(d *Dependencies) { d.Authorizer = nil }},
		{name: "uploads", mutate: func(d *Dependencies) { d.Uploads = nil }},
		{name: "sources", mutate: func(d *Dependencies) { d.Sources = nil }},
		{name: "owner id", mutate: func(d *Dependencies) { d.Config.OwnerID = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deps := validDependencies(t)
			test.mutate(&deps)
			handlers, err := NewHandlers(deps)
			if handlers != nil || !errors.Is(err, errs.ErrMissingDependency) {
				t.Fatalf("NewHandlers = %v, %v", handlers, err)
			}
		})
	}
}

func TestNewHandlersDefaultsKeepaliveInterval(t *testing.T) {
	deps := validDependencies(t)
	deps.Config.KeepaliveInterval = 0
	deps.Clock = clock.NewFake(testNow)
	handlers, err := NewHandlers(deps)
	if err != nil {
		t.Fatalf("NewHandlers: %v", err)
	}
	if handlers.deps.Config.KeepaliveInterval != 15*time.Second {
		t.Fatalf("keepalive = %s", handlers.deps.Config.KeepaliveInterval)
	}
}
