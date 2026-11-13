package main

import (
	"errors"

	"github.com/PRIEYAN/ButterDB/internal/platform/config"
	"github.com/PRIEYAN/ButterDB/internal/platform/datadir"
	"github.com/PRIEYAN/ButterDB/internal/platform/processlock"
)

const (
	exitOK       = 0
	exitFailure  = 1
	exitUsage    = 2
	exitLocked   = 3
	exitKey      = 4
	exitIncompat = 5
	exitTelegram = 6
	exitSigint   = 130
)

var errUsage = errors.New("invalid usage")

func exitCodeFor(err error) int {
	switch {
	case err == nil:
		return exitOK
	case errors.Is(err, processlock.ErrAlreadyRunning):
		return exitLocked
	case errors.Is(err, errUsage),
		errors.Is(err, config.ErrInvalid),
		errors.Is(err, datadir.ErrEmptyRoot),
		errors.Is(err, datadir.ErrNotDirectory),
		errors.Is(err, datadir.ErrLooseMode),
		errors.Is(err, datadir.ErrForeignOwner):
		return exitUsage
	default:
		return exitFailure
	}
}
