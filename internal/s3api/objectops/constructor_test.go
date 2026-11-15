package objectops

import (
	"errors"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/s3api/router"
)

func TestNewHandlersRejectsMissingDependencies(t *testing.T) {
	base := newFixture(t, fixtureOptions{}).handlers.deps
	cases := []struct {
		name   string
		mutate func(*Dependencies)
	}{
		{"evaluator", func(d *Dependencies) { d.Evaluator = nil }},
		{"payloads", func(d *Dependencies) { d.Payloads = nil }},
		{"writer", func(d *Dependencies) { d.Writer = nil }},
		{"remover", func(d *Dependencies) { d.Remover = nil }},
		{"opener", func(d *Dependencies) { d.Opener = nil }},
		{"heads", func(d *Dependencies) { d.Heads = nil }},
		{"principals", func(d *Dependencies) { d.Principals = nil }},
		{"streamer", func(d *Dependencies) { d.Streamer = nil }},
		{"clock", func(d *Dependencies) { d.Clock = nil }},
		{"logger", func(d *Dependencies) { d.Logger = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := base
			tc.mutate(&deps)
			if _, err := NewHandlers(deps); !errors.Is(err, errs.ErrMissingDependency) {
				t.Fatalf("NewHandlers error = %v", err)
			}
		})
	}
}

func TestNewHandlersDefaults(t *testing.T) {
	deps := newFixture(t, fixtureOptions{}).handlers.deps
	deps.Config = Config{}
	handlers, err := NewHandlers(deps)
	if err != nil {
		t.Fatal(err)
	}
	if handlers.deps.Config.BodyIdleTimeout != 60*time.Second || handlers.deps.Config.KeepAliveInterval != 15*time.Second {
		t.Fatalf("config = %+v", handlers.deps.Config)
	}
}

func TestRoutesCoverObjectOperations(t *testing.T) {
	routes := newFixture(t, fixtureOptions{}).handlers.Routes()
	for _, op := range []router.Operation{router.OpPutObject, router.OpGetObject, router.OpHeadObject, router.OpDeleteObject, router.OpDeleteObjects, router.OpCopyObject} {
		if routes[op] == nil {
			t.Fatalf("route for %s missing", op)
		}
	}
	if len(routes) != 6 {
		t.Fatalf("routes = %d", len(routes))
	}
}
