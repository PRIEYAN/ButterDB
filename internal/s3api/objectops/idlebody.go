package objectops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/s3api/s3err"
)

const bodyIdleTimeoutText = "Your socket connection to the server was not read from or written to within the timeout period. Idle connections will be closed."

var errBodyDeadlineUnavailable = errors.New("objectops: request body deadline unavailable")

type idleDeadlineBody struct {
	body        io.ReadCloser
	setDeadline func(time.Time) error
	idleTimeout time.Duration
	clock       clock.Clock
	logger      *slog.Logger
}

func (b *idleDeadlineBody) Read(buffer []byte) (int, error) {
	if err := b.setDeadline(b.clock.Now().Add(b.idleTimeout)); err != nil {
		return 0, bodyDeadlineError("set idle deadline", err)
	}
	n, readErr := b.body.Read(buffer)
	clearErr := b.setDeadline(time.Time{})
	if isBodyReadTimeout(readErr) {
		b.logger.Warn("objectops: request body read idle timeout", "idle_timeout", b.idleTimeout)
		readErr = fmt.Errorf("objectops: request body: %w", s3err.New(s3err.CodeRequestTimeout, bodyIdleTimeoutText))
	}
	if readErr != nil {
		return n, readErr
	}
	if clearErr != nil {
		return n, bodyDeadlineError("clear idle deadline", clearErr)
	}
	return n, nil
}

func (b *idleDeadlineBody) Close() error {
	return b.body.Close()
}

func (h *Handlers) withIdleDeadline(w http.ResponseWriter, r *http.Request, body io.ReadCloser) io.ReadCloser {
	if h.deps.Config.BodyIdleTimeout < 0 || r.Body == nil || r.Body == http.NoBody {
		return body
	}
	return &idleDeadlineBody{
		body:        body,
		setDeadline: http.NewResponseController(w).SetReadDeadline,
		idleTimeout: h.deps.Config.BodyIdleTimeout,
		clock:       h.deps.Clock,
		logger:      h.deps.Logger,
	}
}

func bodyDeadlineError(operation string, err error) error {
	return fmt.Errorf("%w: %s: %v", errBodyDeadlineUnavailable, operation, err)
}

func isBodyReadTimeout(err error) bool {
	if err == nil {
		return false
	}
	var networkError net.Error
	return errors.As(err, &networkError) && networkError.Timeout() && !errors.Is(err, context.DeadlineExceeded)
}
