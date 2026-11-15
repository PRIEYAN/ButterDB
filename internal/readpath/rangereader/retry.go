package rangereader

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"syscall"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

const floodWaitWithoutDuration = 2 * time.Second

var ErrInvalidRetryPolicy = errors.New("rangereader: invalid retry policy")

type Sleeper interface {
	Sleep(ctx context.Context, wait time.Duration) error
}

type SleeperFunc func(ctx context.Context, wait time.Duration) error

func (f SleeperFunc) Sleep(ctx context.Context, wait time.Duration) error {
	return f(ctx, wait)
}

type RetryPolicy struct {
	MaxRetries          int
	MaxWait             time.Duration
	MaxTotalWait        time.Duration
	MaxTransientRetries int
	TransientBackoff    time.Duration
	MaxTransientBackoff time.Duration
	TransientJitter     time.Duration
}

func defaultRangeRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxRetries:          5,
		MaxWait:             2 * time.Minute,
		MaxTotalWait:        5 * time.Minute,
		MaxTransientRetries: 3,
		TransientBackoff:    250 * time.Millisecond,
		MaxTransientBackoff: 3 * time.Second,
		TransientJitter:     100 * time.Millisecond,
	}
}

func (p RetryPolicy) validate() error {
	if p.MaxRetries < 0 || p.MaxWait < 0 || p.MaxTotalWait < 0 ||
		p.MaxTransientRetries < 0 || p.TransientBackoff < 0 || p.MaxTransientBackoff < 0 || p.TransientJitter < 0 {
		return fmt.Errorf("%w: limits must be non-negative", ErrInvalidRetryPolicy)
	}
	if p.MaxRetries > 0 && (p.MaxWait == 0 || p.MaxTotalWait == 0) {
		return fmt.Errorf("%w: retrying requires per-wait and total-wait limits", ErrInvalidRetryPolicy)
	}
	if p.MaxTransientRetries > 0 && p.TransientBackoff == 0 {
		return fmt.Errorf("%w: retrying transient failures requires a backoff", ErrInvalidRetryPolicy)
	}
	return nil
}

type retryer struct {
	policy  RetryPolicy
	sleeper Sleeper
	random  io.Reader
	logger  *slog.Logger
}

type retryRun struct {
	retryer
	totalWait        time.Duration
	floodRetries     int
	transientRetries int
}

func (t retryer) do(ctx context.Context, action func() error) error {
	run := retryRun{retryer: t}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := action()
		if err == nil {
			return nil
		}
		retry, waitErr := run.retryAfter(ctx, err)
		if waitErr != nil {
			return waitErr
		}
		if !retry {
			return err
		}
	}
}

func (r *retryRun) retryAfter(ctx context.Context, err error) (bool, error) {
	if wait, floodWait := floodWaitDuration(err); floodWait {
		return r.retryFloodWait(ctx, wait)
	}
	return r.retryTransient(ctx, err)
}

func (r *retryRun) retryFloodWait(ctx context.Context, wait time.Duration) (bool, error) {
	p := r.policy
	exhausted := wait < 0 || r.floodRetries >= p.MaxRetries ||
		(p.MaxWait > 0 && wait > p.MaxWait) ||
		(p.MaxTotalWait > 0 && wait > p.MaxTotalWait-r.totalWait)
	if exhausted {
		r.logger.Warn("rangereader: flood wait retry budget exhausted", "retries", r.floodRetries, "wait", wait, "max_retries", p.MaxRetries)
		return false, nil
	}
	r.logger.Warn("rangereader: flood wait, sleeping before retry", "attempt", r.floodRetries+1, "wait", wait)
	if err := r.sleeper.Sleep(ctx, wait); err != nil {
		return false, err
	}
	r.totalWait += wait
	r.floodRetries++
	return true, nil
}

func (r *retryRun) retryTransient(ctx context.Context, err error) (bool, error) {
	p := r.policy
	if !isTransientTransport(err) || r.transientRetries >= p.MaxTransientRetries {
		return false, nil
	}
	backoff := r.transientBackoff(r.transientRetries)
	r.logger.Warn("rangereader: transient transport failure, retrying", "attempt", r.transientRetries+1, "max_retries", p.MaxTransientRetries, "backoff", backoff)
	if sleepErr := r.sleeper.Sleep(ctx, backoff); sleepErr != nil {
		return false, sleepErr
	}
	r.transientRetries++
	return true, nil
}

func (t retryer) transientBackoff(retry int) time.Duration {
	backoff := t.policy.TransientBackoff
	maxBackoff := t.policy.MaxTransientBackoff
	if maxBackoff == 0 {
		return saturatingDurationAdd(backoff, randomJitter(t.random, t.policy.TransientJitter))
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
	jitterRoom := min(maxBackoff-backoff, t.policy.TransientJitter)
	return backoff + randomJitter(t.random, jitterRoom)
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

func floodWaitDuration(err error) (time.Duration, bool) {
	if err == nil {
		return 0, false
	}
	var typed *tgclient.FloodWaitError
	if errors.As(err, &typed) {
		return typed.Duration, true
	}
	if errors.Is(err, tgclient.ErrFloodWait) {
		return floodWaitWithoutDuration, true
	}
	return 0, false
}

// gotd cancels its own internal contexts when the engine dies, so these wrap context.Canceled while the caller's context is alive.
var transientTransportPatterns = []string{
	"engine forcibly closed",
	"engine was closed",
	"use of closed network connection",
	"connection reset",
	"connection aborted",
	"connection refused",
	"connection dead",
	"dc is closed",
	"dc closed",
	"broken pipe",
	"unexpected eof",
	"i/o timeout",
	"tls handshake timeout",
	"no such host",
	"network is unreachable",
}

func isTransientTransport(err error) bool {
	if err == nil {
		return false
	}
	if _, floodWait := floodWaitDuration(err); floodWait {
		return false
	}
	if isTypedTransientTransport(err) {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, pattern := range transientTransportPatterns {
		if strings.Contains(message, pattern) {
			return true
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func isTypedTransientTransport(err error) bool {
	typed := []error{
		io.ErrUnexpectedEOF,
		net.ErrClosed,
		syscall.ECONNRESET,
		syscall.ECONNABORTED,
		syscall.ECONNREFUSED,
		syscall.EPIPE,
		syscall.ETIMEDOUT,
		syscall.ENETUNREACH,
		syscall.EHOSTUNREACH,
	}
	for _, target := range typed {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}
