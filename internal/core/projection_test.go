package core

import (
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
)

func TestNewProjection(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tests := []struct {
		name    string
		deps    ProjectionDependencies
		wantErr error
	}{
		{name: "wires projector and rebuilder", deps: ProjectionDependencies{Logger: logger}},
		{name: "rejects a nil logger", deps: ProjectionDependencies{}, wantErr: errs.ErrMissingDependency},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewProjection(tt.deps)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				return
			}
			if got.Projector == nil || got.Rebuilder == nil || got.Generations == nil {
				t.Fatalf("projection has a nil member: %+v", got)
			}
		})
	}
}

func TestKeyGenerationsAdvanceIsPerBucket(t *testing.T) {
	generations := NewKeyGenerations()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			generations.Advance(10)
		}()
	}
	wg.Wait()
	generations.Advance(20)
	tests := []struct {
		name   string
		bucket int64
		want   uint64
	}{
		{name: "advanced eight times", bucket: 10, want: 8},
		{name: "advanced once", bucket: 20, want: 1},
		{name: "never advanced", bucket: 30, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := generations.Generation(tt.bucket); got != tt.want {
				t.Fatalf("generation = %d, want %d", got, tt.want)
			}
		})
	}
}
