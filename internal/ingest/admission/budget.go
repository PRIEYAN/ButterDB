package admission

import (
	"fmt"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
)

func NewBudget(now time.Time, total time.Duration) Budget {
	return Budget{Deadline: now.Add(total)}
}

func (b Budget) Remaining(now time.Time) time.Duration {
	return b.Deadline.Sub(now)
}

func (e *SlowDownError) Error() string {
	return fmt.Sprintf("admission: slow down: %s: retry after %s", e.Reason, e.RetryAfter)
}

func (e *SlowDownError) Unwrap() error {
	return errs.ErrSlowDown
}

func newSlowDown(reason string, wait time.Duration) *SlowDownError {
	return &SlowDownError{RetryAfter: roundUpSeconds(wait), Reason: reason}
}

func roundUpSeconds(wait time.Duration) time.Duration {
	seconds := (wait + time.Second - 1) / time.Second
	return max(seconds, 1) * time.Second
}
