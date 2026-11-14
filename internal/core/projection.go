package core

import (
	"fmt"
	"log/slog"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/projection/apply"
	projlog "github.com/PRIEYAN/ButterDB/internal/projection/log"
	"github.com/PRIEYAN/ButterDB/internal/projection/rebuild"
)

type ProjectionDependencies struct {
	Logger *slog.Logger
}

type Projection struct {
	Projector   *projlog.Projector
	Rebuilder   *rebuild.Rebuilder
	Generations *KeyGenerations
}

func NewProjection(deps ProjectionDependencies) (*Projection, error) {
	if deps.Logger == nil {
		return nil, fmt.Errorf("core: %w: logger", errs.ErrMissingDependency)
	}
	generations := NewKeyGenerations()
	registry, err := apply.NewRegistry(apply.Dependencies{Generation: generations})
	if err != nil {
		return nil, fmt.Errorf("core: projection registry: %w", err)
	}
	projector, err := projlog.NewProjector(projlog.Dependencies{Registry: registry, Logger: deps.Logger})
	if err != nil {
		return nil, fmt.Errorf("core: projector: %w", err)
	}
	rebuilder, err := rebuild.New(rebuild.Dependencies{Registry: registry, Logger: deps.Logger})
	if err != nil {
		return nil, fmt.Errorf("core: rebuilder: %w", err)
	}
	return &Projection{Projector: projector, Rebuilder: rebuilder, Generations: generations}, nil
}
