package spool

import (
	"errors"
	"fmt"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
)

var (
	ErrInvalidRequest  = errors.New("spool: invalid request")
	ErrLengthMismatch  = errors.New("spool: body length differs from declared length")
	ErrJobFileExists   = errors.New("spool: job file already exists")
	ErrNotFound        = errors.New("spool: file not found")
	ErrFileCorrupt     = errors.New("spool: file does not match its record")
	ErrBudgetExhausted = fmt.Errorf("spool: budget exhausted: %w", errs.ErrSlowDown)
)
