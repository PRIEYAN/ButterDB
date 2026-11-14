package flow

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"syscall"

	"github.com/gotd/td/pool"
	"github.com/gotd/td/rpc"
)

var ErrScopeClosed = errors.New("tgclient: connection scope closed")

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

func IsTransientTransport(err error) bool {
	if err == nil {
		return false
	}
	if _, floodWait := FloodWaitDuration(err); floodWait {
		return false
	}
	if errors.Is(err, ErrScopeClosed) {
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
		rpc.ErrEngineClosed,
		pool.ErrConnDead,
		io.ErrUnexpectedEOF,
		net.ErrClosed,
		syscall.ECONNRESET,
		syscall.ECONNABORTED,
		syscall.ECONNREFUSED,
		syscall.EPIPE,
		syscall.ETIMEDOUT,
		syscall.ENETRESET,
		syscall.ENETDOWN,
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
