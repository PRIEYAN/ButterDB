package apply

import (
	"errors"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/projection"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

func TestRegistryLookup(t *testing.T) {
	e := newEnv(t)
	var registry projection.Registry = e.registry
	known := []wire.OpType{wire.OpBody, wire.OpPut, wire.OpDel, wire.OpRegistry, wire.OpBucketCreate, wire.OpBucketConfig, wire.OpBucketDelete, wire.OpEncConfig}
	for _, op := range known {
		t.Run("known "+string(op), func(t *testing.T) {
			if applier, ok := registry.Lookup(op); !ok || applier == nil {
				t.Fatalf("no applier for %q", op)
			}
		})
	}
	for _, op := range []wire.OpType{"part", "manifest", "mkdir", "", "zzz"} {
		t.Run("unknown "+string(op), func(t *testing.T) {
			if applier, ok := registry.Lookup(op); ok || applier != nil {
				t.Fatalf("unexpected applier for unknown op %q", op)
			}
		})
	}
}

func TestNewRegistryRejectsMissingDependency(t *testing.T) {
	registry, err := NewRegistry(Dependencies{})
	if !errors.Is(err, errs.ErrMissingDependency) || registry != nil {
		t.Fatalf("got %v, %v; want ErrMissingDependency", registry, err)
	}
}
