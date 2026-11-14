package flow

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"
)

const (
	defaultWriteFloodWaitRetries = 2
	defaultWriteFloodWaitMax     = 30 * time.Second
	defaultWriteFloodWaitTotal   = 60 * time.Second

	defaultTransientRetries    = 4
	defaultTransientBackoff    = 2 * time.Second
	defaultTransientMaxBackoff = 30 * time.Second
	defaultTransientJitter     = 750 * time.Millisecond
)

var ErrInvalidFloodWaitRetryPolicy = errors.New("flow: invalid flood wait retry policy")

type FloodWaitRetryPolicy struct {
	WaitUntilCanceled           bool
	MaxRetries                  int
	MaxWait                     time.Duration
	MaxTotalWait                time.Duration
	RetryTransportUntilCanceled bool
	MaxTransientRetries         int
	TransientBackoff            time.Duration
	MaxTransientBackoff         time.Duration
	TransientJitter             time.Duration

	Sleeper Sleeper
	Random  io.Reader
	Logger  *slog.Logger
}

func DefaultWriteFloodWaitRetryPolicy() FloodWaitRetryPolicy {
	return FloodWaitRetryPolicy{
		MaxRetries:          defaultWriteFloodWaitRetries,
		MaxWait:             defaultWriteFloodWaitMax,
		MaxTotalWait:        defaultWriteFloodWaitTotal,
		MaxTransientRetries: defaultTransientRetries,
		TransientBackoff:    defaultTransientBackoff,
		MaxTransientBackoff: defaultTransientMaxBackoff,
		TransientJitter:     defaultTransientJitter,
	}
}

func DefaultUploadFloodWaitRetryPolicy() FloodWaitRetryPolicy {
	policy := DefaultWriteFloodWaitRetryPolicy()
	policy.WaitUntilCanceled = true
	policy.MaxRetries = 0
	policy.MaxWait = 0
	policy.MaxTotalWait = 0
	return policy
}

func DefaultUploadNetworkRetryPolicy() FloodWaitRetryPolicy {
	policy := DefaultUploadFloodWaitRetryPolicy()
	policy.RetryTransportUntilCanceled = true
	policy.MaxTransientRetries = 0
	return policy
}

func (p FloodWaitRetryPolicy) Validate() error {
	if p.MaxRetries < 0 || p.MaxWait < 0 || p.MaxTotalWait < 0 ||
		p.MaxTransientRetries < 0 || p.TransientBackoff < 0 || p.MaxTransientBackoff < 0 || p.TransientJitter < 0 {
		return fmt.Errorf("%w: limits must be non-negative", ErrInvalidFloodWaitRetryPolicy)
	}
	if !p.WaitUntilCanceled && p.MaxRetries > 0 && (p.MaxWait == 0 || p.MaxTotalWait == 0) {
		return fmt.Errorf("%w: retrying requires per-wait and total-wait limits", ErrInvalidFloodWaitRetryPolicy)
	}
	if (p.MaxTransientRetries > 0 || p.RetryTransportUntilCanceled) && p.TransientBackoff == 0 {
		return fmt.Errorf("%w: retrying transient failures requires a backoff", ErrInvalidFloodWaitRetryPolicy)
	}
	return nil
}

func (p FloodWaitRetryPolicy) Do(ctx context.Context, action func() error) error {
	if ctx == nil {
		return fmt.Errorf("%w: context is required", ErrInvalidFloodWaitRetryPolicy)
	}
	if action == nil {
		return fmt.Errorf("%w: action is required", ErrInvalidFloodWaitRetryPolicy)
	}
	if err := p.Validate(); err != nil {
		return err
	}
	run := retryRun{policy: p, sleeper: p.Sleeper, logger: p.Logger}
	if run.sleeper == nil {
		run.sleeper = SystemSleeper{}
	}
	if run.logger == nil {
		run.logger = slog.New(slog.DiscardHandler)
	}
	return run.do(ctx, action)
}

type retryRun struct {
	policy           FloodWaitRetryPolicy
	sleeper          Sleeper
	logger           *slog.Logger
	totalWait        time.Duration
	floodRetries     int
	transientRetries int
}

func (r *retryRun) do(ctx context.Context, action func() error) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := action()
		if err == nil {
			return nil
		}
		retry, waitErr := r.retryAfter(ctx, err)
		if waitErr != nil {
			return waitErr
		}
		if !retry {
			return err
		}
	}
}

func (r *retryRun) retryAfter(ctx context.Context, err error) (bool, error) {
	if wait, floodWait := FloodWaitDuration(err); floodWait {
		return r.retryFloodWait(ctx, wait)
	}
	return r.retryTransient(ctx, err)
}

func (r *retryRun) retryFloodWait(ctx context.Context, wait time.Duration) (bool, error) {
	p := r.policy
	exhausted := wait < 0 || (!p.WaitUntilCanceled && (r.floodRetries >= p.MaxRetries ||
		(p.MaxWait > 0 && wait > p.MaxWait) ||
		(p.MaxTotalWait > 0 && wait > p.MaxTotalWait-r.totalWait)))
	if exhausted {
		r.logger.Warn("flow: flood wait retry budget exhausted", "retries", r.floodRetries, "wait", wait, "max_retries", p.MaxRetries)
		return false, nil
	}
	r.logger.Warn("flow: flood wait, sleeping before retry", "attempt", r.floodRetries+1, "wait", wait)
	if err := r.sleeper.Sleep(ctx, wait); err != nil {
		return false, err
	}
	if !p.WaitUntilCanceled {
		r.totalWait += wait
	}
	r.floodRetries++
	return true, nil
}

func (r *retryRun) retryTransient(ctx context.Context, err error) (bool, error) {
	p := r.policy
	if !IsTransientTransport(err) || (!p.RetryTransportUntilCanceled && r.transientRetries >= p.MaxTransientRetries) {
		return false, nil
	}
	backoff := p.transientBackoff(r.transientRetries)
	r.logger.Warn("flow: transient transport failure, retrying",
		"attempt", r.transientRetries+1,
		"max_retries", p.MaxTransientRetries,
		"retry_until_canceled", p.RetryTransportUntilCanceled,
		"backoff", backoff,
	)
	if sleepErr := r.sleeper.Sleep(ctx, backoff); sleepErr != nil {
		return false, sleepErr
	}
	r.transientRetries++
	return true, nil
}

func (p FloodWaitRetryPolicy) transientBackoff(retry int) time.Duration {
	backoff := p.TransientBackoff
	maxBackoff := p.MaxTransientBackoff
	if maxBackoff == 0 {
		return saturatingDurationAdd(backoff, randomJitter(p.Random, p.TransientJitter))
	}
	if backoff >= maxBackoff {
		return maxBackoff
	}
	for i := 0; i < retry; i++ {
		if backoff > maxBackoff-backoff {
			return maxBackoff
		}
		backoff *= 2
	}
	jitterRoom := maxBackoff - backoff
	if p.TransientJitter < jitterRoom {
		jitterRoom = p.TransientJitter
	}
	return backoff + randomJitter(p.Random, jitterRoom)
}

func saturatingDurationAdd(base, extra time.Duration) time.Duration {
	const maxDuration = time.Duration(1<<63 - 1)
	if extra <= 0 {
		return base
	}
	if base > maxDuration-extra {
		return maxDuration
	}
	return base + extra
}

func randomJitter(random io.Reader, maxJitter time.Duration) time.Duration {
	if maxJitter <= 0 || random == nil {
		return 0
	}
	var raw [8]byte
	if _, err := io.ReadFull(random, raw[:]); err != nil {
		return 0
	}
	return time.Duration(binary.BigEndian.Uint64(raw[:]) % (uint64(maxJitter) + 1))
}
