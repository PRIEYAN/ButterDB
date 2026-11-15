package rangereader

import (
	"context"
	"sync"
)

type flight struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	data   []byte
	err    error

	doc         string
	start       int64
	background  bool
	speculative bool

	claimed  chan struct{}
	promoted chan struct{}
	once     [2]sync.Once
}

func newFlight(parent context.Context, doc string, start int64, background, speculative bool) *flight {
	ctx, cancel := context.WithCancel(parent)
	return &flight{
		ctx:         ctx,
		cancel:      cancel,
		done:        make(chan struct{}),
		doc:         doc,
		start:       start,
		background:  background,
		speculative: speculative,
		claimed:     make(chan struct{}),
		promoted:    make(chan struct{}),
	}
}

func (f *flight) claim(foreground bool) {
	f.once[0].Do(func() { close(f.claimed) })
	if foreground {
		f.once[1].Do(func() { close(f.promoted) })
	}
}

func (f *flight) isClaimed() bool {
	return channelClosed(f.claimed)
}

func (f *flight) isPromoted() bool {
	return channelClosed(f.promoted)
}

func channelClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}
