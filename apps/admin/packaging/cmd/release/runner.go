package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

type commandRunner interface {
	Run(ctx context.Context, cmd command) error
	Output(ctx context.Context, cmd command) (string, error)
}

type execRunner struct {
	stdout io.Writer
	stderr io.Writer
}

func (r execRunner) Run(ctx context.Context, cmd command) error {
	process := r.prepare(ctx, cmd)
	process.Stdout = r.stdout
	process.Stderr = r.stderr
	if err := process.Run(); err != nil {
		return fmt.Errorf("release: run %s: %w", cmd.Name, err)
	}
	return nil
}

func (r execRunner) Output(ctx context.Context, cmd command) (string, error) {
	process := r.prepare(ctx, cmd)
	var stdout bytes.Buffer
	process.Stdout = &stdout
	if err := process.Run(); err != nil {
		return "", fmt.Errorf("release: run %s: %w", cmd.Name, err)
	}
	return strings.TrimSpace(stdout.String()), nil
}

func (execRunner) prepare(ctx context.Context, cmd command) *exec.Cmd {
	process := exec.CommandContext(ctx, cmd.Name, cmd.Args...)
	process.Dir = cmd.Dir
	process.Env = append(os.Environ(), cmd.Env...)
	return process
}
