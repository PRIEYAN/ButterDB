package conn

import (
	"context"
	"errors"
	"log/slog"
	"sync"
)

var errScopeClosed = errors.New("conn: connection scope closed")

type liveConn struct {
	scopeFn func(ctx context.Context, ready func()) error
	logger  *slog.Logger

	mu     sync.Mutex
	closed bool
	scope  *connScope
}

type connScope struct {
	cancel  context.CancelFunc
	readyCh chan struct{}
	doneCh  chan struct{}
	ready   bool
	done    bool
	err     error
}

func newLiveConn(scopeFn func(ctx context.Context, ready func()) error, logger *slog.Logger) *liveConn {
	return &liveConn{scopeFn: scopeFn, logger: logger}
}

func (l *liveConn) acquire(ctx context.Context) error {
	for {
		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			return errScopeClosed
		}
		if l.scope == nil || l.scope.done {
			l.start()
		}
		scope := l.scope
		readyCh, doneCh := scope.readyCh, scope.doneCh
		l.mu.Unlock()

		select {
		case <-readyCh:
			retry, err := l.afterReady(scope)
			if retry {
				continue
			}
			return err
		case <-doneCh:
			retry, err := l.afterDone(scope)
			if retry {
				continue
			}
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (l *liveConn) afterReady(scope *connScope) (retry bool, err error) {
	l.mu.Lock()
	if l.scope != scope {
		l.mu.Unlock()
		return true, nil
	}
	if !scope.done {
		l.mu.Unlock()
		return false, nil
	}
	l.mu.Unlock()
	return l.afterDone(scope)
}

func (l *liveConn) afterDone(scope *connScope) (retry bool, err error) {
	l.mu.Lock()
	if l.scope != scope {
		l.mu.Unlock()
		return true, nil
	}
	wasReady, scopeErr := scope.ready, scope.err
	l.scope = nil
	l.mu.Unlock()
	if wasReady {
		return true, nil
	}
	if scopeErr == nil {
		scopeErr = errScopeClosed
	}
	return false, scopeErr
}

func (l *liveConn) start() {
	runCtx, cancel := context.WithCancel(context.Background())
	scope := &connScope{
		cancel:  cancel,
		readyCh: make(chan struct{}),
		doneCh:  make(chan struct{}),
	}
	l.scope = scope

	var once sync.Once
	ready := func() {
		once.Do(func() {
			l.mu.Lock()
			scope.ready = true
			l.mu.Unlock()
			close(scope.readyCh)
		})
	}

	go func() {
		err := l.scopeFn(runCtx, ready)
		if err != nil && !l.isClosed() {
			l.logger.Warn("conn: connection scope exited", "error", err)
		}
		l.mu.Lock()
		scope.err = err
		scope.done = true
		l.mu.Unlock()
		cancel()
		close(scope.doneCh)
	}()
}

func (l *liveConn) isClosed() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.closed
}

func (l *liveConn) Close() {
	_ = l.closeWait(context.Background())
}

func (l *liveConn) closeWait(ctx context.Context) error {
	l.mu.Lock()
	l.closed = true
	scope := l.scope
	l.scope = nil
	l.mu.Unlock()

	if scope == nil {
		return nil
	}
	scope.cancel()
	select {
	case <-scope.doneCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
