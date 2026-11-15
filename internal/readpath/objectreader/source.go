package objectreader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/readpath"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

const (
	maxConcurrentDocumentResolutions = 4
	maxCachedPeers                   = 8
	maxCachedDocumentRefs            = 384
	documentRefCacheTTL              = 3 * time.Minute
)

type SourceDependencies struct {
	Peers     PeerResolver
	Documents DocumentResolver
	Stored    readpath.StoredReader
	Clock     clock.Clock
}

func (d SourceDependencies) validate() error {
	switch {
	case d.Peers == nil:
		return fmt.Errorf("objectreader: %w: peers", errs.ErrMissingDependency)
	case d.Documents == nil:
		return fmt.Errorf("objectreader: %w: documents", errs.ErrMissingDependency)
	case d.Stored == nil:
		return fmt.Errorf("objectreader: %w: stored reader", errs.ErrMissingDependency)
	case d.Clock == nil:
		return fmt.Errorf("objectreader: %w: clock", errs.ErrMissingDependency)
	}
	return nil
}

type documentRefKey struct {
	peer       tgclient.InputPeer
	msgID      int64
	storedSize int64
}

func newDocumentRefKey(peer tgclient.InputPeer, meta object.ObjectMeta) documentRefKey {
	return documentRefKey{peer: peer, msgID: meta.BodyMsgID, storedSize: meta.StoredSize}
}

type StoredSource struct {
	deps                SourceDependencies
	mu                  sync.RWMutex
	closed              bool
	lifetime            context.Context
	cancelLifetime      context.CancelFunc
	resolveSlots        chan struct{}
	peerCache           *LRU[int64, tgclient.InputPeer]
	peerResolutions     *Coalescer[int64, tgclient.InputPeer]
	documentCache       *LRU[documentRefKey, tgclient.DocumentRef]
	documentResolutions *Coalescer[documentRefKey, tgclient.DocumentRef]
}

var _ Source = (*StoredSource)(nil)

func NewStoredSource(deps SourceDependencies) (*StoredSource, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	lifetime, cancelLifetime := context.WithCancel(context.Background())
	return &StoredSource{
		deps:                deps,
		lifetime:            lifetime,
		cancelLifetime:      cancelLifetime,
		resolveSlots:        make(chan struct{}, maxConcurrentDocumentResolutions),
		peerCache:           newPeerCache(maxCachedPeers),
		peerResolutions:     NewCoalescer[int64, tgclient.InputPeer](0),
		documentCache:       newDocumentRefCache(maxCachedDocumentRefs, documentRefCacheTTL, deps.Clock),
		documentResolutions: NewCoalescer[documentRefKey, tgclient.DocumentRef](0),
	}, nil
}

func (s *StoredSource) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.peerResolutions.Close(ErrClosed)
	s.peerCache.Clear()
	s.documentResolutions.Close(ErrClosed)
	s.documentCache.Clear()
	s.mu.Unlock()

	s.cancelLifetime()
	return errors.Join(s.peerResolutions.Wait(ctx), s.documentResolutions.Wait(ctx))
}

func (s *StoredSource) ensureOpen() error {
	s.mu.RLock()
	closed := s.closed
	s.mu.RUnlock()
	if closed {
		return ErrClosed
	}
	return nil
}

func (s *StoredSource) Done() <-chan struct{} {
	return s.lifetime.Done()
}

func (s *StoredSource) ResolveBody(ctx context.Context, meta object.ObjectMeta) (tgclient.DocumentRef, error) {
	peer, err := s.resolvePeer(ctx, meta.ChannelID)
	if err != nil {
		return tgclient.DocumentRef{}, fmt.Errorf("objectreader: resolve bucket channel: %w", err)
	}
	ref, err := s.resolveDocument(ctx, peer, meta)
	if err != nil {
		return tgclient.DocumentRef{}, fmt.Errorf("objectreader: resolve body message %d: %w", meta.BodyMsgID, err)
	}
	return ref, nil
}

func (s *StoredSource) ReadDocumentRange(ctx context.Context, meta object.ObjectMeta, offset int64, dst []byte) (int, error) {
	if ctx == nil {
		return 0, ErrNilContext
	}
	if offset < 0 {
		return 0, ErrNegativeOffset
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := s.ensureOpen(); err != nil {
		return 0, err
	}
	if len(dst) == 0 {
		return 0, nil
	}
	if offset >= meta.StoredSize {
		return 0, io.EOF
	}
	want := len(dst)
	if remaining := meta.StoredSize - offset; int64(want) > remaining {
		want = int(remaining)
	}
	ref, err := s.ResolveBody(ctx, meta)
	if err != nil {
		return 0, err
	}
	n, err := s.deps.Stored.ReadStoredAt(ctx, ref, dst[:want], offset)
	if err != nil && !(errors.Is(err, io.EOF) && n == want) {
		return n, err
	}
	if n != want {
		return n, io.ErrUnexpectedEOF
	}
	if want < len(dst) {
		return n, io.EOF
	}
	return n, nil
}

func (s *StoredSource) resolvePeer(ctx context.Context, channelID int64) (tgclient.InputPeer, error) {
	if ctx == nil {
		return tgclient.InputPeer{}, ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return tgclient.InputPeer{}, err
	}
	if err := s.ensureOpen(); err != nil {
		return tgclient.InputPeer{}, err
	}
	peer, err := s.peerResolutions.Load(
		ctx,
		s.lifetime,
		channelID,
		func() (tgclient.InputPeer, bool) { return s.peerCache.Get(channelID) },
		func(loadContext context.Context) (tgclient.InputPeer, error) {
			return s.deps.Peers.ResolvePeer(loadContext, channelID)
		},
		func(resolved tgclient.InputPeer) { s.peerCache.Put(channelID, resolved) },
	)
	if openErr := s.ensureOpen(); openErr != nil {
		return tgclient.InputPeer{}, openErr
	}
	return peer, err
}

func (s *StoredSource) resolveDocument(ctx context.Context, peer tgclient.InputPeer, meta object.ObjectMeta) (tgclient.DocumentRef, error) {
	if ctx == nil {
		return tgclient.DocumentRef{}, ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return tgclient.DocumentRef{}, err
	}
	if err := s.ensureOpen(); err != nil {
		return tgclient.DocumentRef{}, err
	}
	key := newDocumentRefKey(peer, meta)
	ref, err := s.documentResolutions.Load(
		ctx,
		s.lifetime,
		key,
		func() (tgclient.DocumentRef, bool) { return s.documentCache.Get(key) },
		func(loadContext context.Context) (tgclient.DocumentRef, error) {
			return s.resolveDocumentUncached(loadContext, peer, meta)
		},
		func(resolved tgclient.DocumentRef) { s.documentCache.Put(key, resolved) },
	)
	if openErr := s.ensureOpen(); openErr != nil {
		return tgclient.DocumentRef{}, openErr
	}
	return cloneDocumentRef(ref), err
}

func (s *StoredSource) resolveDocumentUncached(ctx context.Context, peer tgclient.InputPeer, meta object.ObjectMeta) (tgclient.DocumentRef, error) {
	select {
	case s.resolveSlots <- struct{}{}:
		defer func() { <-s.resolveSlots }()
	case <-ctx.Done():
		return tgclient.DocumentRef{}, ctx.Err()
	}
	if err := s.ensureOpen(); err != nil {
		return tgclient.DocumentRef{}, err
	}
	if err := ctx.Err(); err != nil {
		return tgclient.DocumentRef{}, err
	}
	ref, err := s.deps.Documents.ResolveDocument(ctx, peer, meta.BodyMsgID)
	if err != nil {
		return tgclient.DocumentRef{}, err
	}
	if err := validateDocumentIdentity(peer, meta, ref); err != nil {
		return tgclient.DocumentRef{}, err
	}
	return ref, nil
}

func validateDocumentIdentity(expectedPeer tgclient.InputPeer, meta object.ObjectMeta, ref tgclient.DocumentRef) error {
	if ref.MsgID != meta.BodyMsgID {
		return fmt.Errorf("%w: message identity requested=%d resolved=%d", ErrDocumentMismatch, meta.BodyMsgID, ref.MsgID)
	}
	if ref.Peer != expectedPeer {
		return fmt.Errorf("%w: peer identity for message %d", ErrDocumentMismatch, meta.BodyMsgID)
	}
	if ref.Size != meta.StoredSize {
		return fmt.Errorf("%w: message %d size mismatch: projection=%d telegram=%d", ErrDocumentMismatch, meta.BodyMsgID, meta.StoredSize, ref.Size)
	}
	return nil
}

func newPeerCache(capacity int) *LRU[int64, tgclient.InputPeer] {
	return NewLRU[int64, tgclient.InputPeer](LRUConfig[tgclient.InputPeer]{Capacity: capacity})
}

func newDocumentRefCache(capacity int, ttl time.Duration, clk clock.Clock) *LRU[documentRefKey, tgclient.DocumentRef] {
	if ttl <= 0 {
		capacity = 0
	}
	return NewLRU[documentRefKey, tgclient.DocumentRef](LRUConfig[tgclient.DocumentRef]{
		Capacity: capacity,
		TTL:      ttl,
		Clock:    clk,
		Clone:    cloneDocumentRef,
	})
}

func cloneDocumentRef(ref tgclient.DocumentRef) tgclient.DocumentRef {
	ref.FileReference = append([]byte(nil), ref.FileReference...)
	return ref
}
