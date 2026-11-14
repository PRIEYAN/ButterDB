package scan

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"syscall"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

const (
	defaultPageSize       = 100
	maxFloodWaitRetries   = 5
	maxFloodWaitSleep     = 60 * time.Second
	maxTransportRetries   = 4
	transportBackoffFirst = 2 * time.Second
	transportBackoffCap   = 30 * time.Second
	transportJitterMax    = 750 * time.Millisecond
	floodWaitUnspecified  = 2 * time.Second
)

func transientPatterns() []string {
	return []string{
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
}

func transientErrnos() []error {
	return []error{
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
}

func floodWaitOf(err error) (time.Duration, bool) {
	var typed *tgclient.FloodWaitError
	if errors.As(err, &typed) {
		return typed.Duration, true
	}
	if errors.Is(err, tgclient.ErrFloodWait) {
		return floodWaitUnspecified, true
	}
	return 0, false
}

// gotd cancels its own internal contexts when its engine dies, so a shutdown wraps context.Canceled while the caller's context is still alive.
func isTransientTransport(err error) bool {
	if err == nil {
		return false
	}
	if _, flood := floodWaitOf(err); flood {
		return false
	}
	for _, target := range transientErrnos() {
		if errors.Is(err, target) {
			return true
		}
	}
	message := strings.ToLower(err.Error())
	for _, pattern := range transientPatterns() {
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

func (s *Scanner) readWithRetry(ctx context.Context, channelID int64, what string, call func() error) error {
	floodRetries, transportRetries := 0, 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := call()
		if err == nil {
			return nil
		}
		if wait, flood := floodWaitOf(err); flood {
			if floodRetries >= maxFloodWaitRetries {
				return err
			}
			floodRetries++
			wait = min(wait, maxFloodWaitSleep)
			s.logger.Warn("scan: flood wait, retrying", "what", what, "channel_id", channelID, "attempt", floodRetries, "wait", wait)
			if sleepErr := s.sleeper.Sleep(ctx, wait); sleepErr != nil {
				return sleepErr
			}
			continue
		}
		if !isTransientTransport(err) || transportRetries >= maxTransportRetries {
			return err
		}
		backoff := s.transportBackoff(transportRetries)
		transportRetries++
		s.logger.Warn("scan: transport failure, retrying", "what", what, "channel_id", channelID, "attempt", transportRetries, "backoff", backoff)
		if sleepErr := s.sleeper.Sleep(ctx, backoff); sleepErr != nil {
			return sleepErr
		}
	}
}

func (s *Scanner) transportBackoff(retry int) time.Duration {
	backoff := transportBackoffFirst
	for i := 0; i < retry && backoff < transportBackoffCap; i++ {
		backoff *= 2
	}
	if backoff >= transportBackoffCap {
		return transportBackoffCap
	}
	room := min(transportBackoffCap-backoff, transportJitterMax)
	return backoff + s.jitter(room)
}

func (s *Scanner) jitter(maxJitter time.Duration) time.Duration {
	if maxJitter <= 0 {
		return 0
	}
	var raw [8]byte
	if _, err := io.ReadFull(s.random, raw[:]); err != nil {
		return 0
	}
	return time.Duration(binary.BigEndian.Uint64(raw[:]) % (uint64(maxJitter) + 1))
}

func (s *Scanner) getHistory(ctx context.Context, channelID int64, peer tgclient.InputPeer, offsetID int64) ([]tgclient.HistoryMessage, error) {
	var page []tgclient.HistoryMessage
	err := s.readWithRetry(ctx, channelID, "history read", func() error {
		var err error
		page, err = s.history.GetHistory(ctx, peer, offsetID, defaultPageSize)
		return err
	})
	return page, err
}

// A pts that cannot be sampled still leaves history scanning viable, so only caller cancellation is reported.
func (s *Scanner) samplePts(ctx context.Context, channelID int64, peer tgclient.InputPeer) (int, error) {
	var pts int
	err := s.readWithRetry(ctx, channelID, "pts read", func() error {
		var err error
		pts, err = s.history.GetChannelPts(ctx, peer)
		return err
	})
	if err == nil {
		return pts, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return 0, ctxErr
	}
	s.logger.Warn("scan: pts unavailable, continuing without it", "channel_id", channelID, "error", err)
	return 0, nil
}
