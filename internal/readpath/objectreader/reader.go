package objectreader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/PRIEYAN/ButterDB/internal/crypto/randomaccess"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/readpath"
)

type Dependencies struct {
	Peers     PeerResolver
	Documents DocumentResolver
	Stored    readpath.StoredReader
	Keys      KeyProvider
	Clock     clock.Clock
}

type Opener struct {
	source           *StoredSource
	keys             KeyProvider
	mu               sync.RWMutex
	closed           bool
	lifetime         context.Context
	cancelLifetime   context.CancelFunc
	encryptedReaders map[*Reader]struct{}
}

var _ Service = (*Opener)(nil)

func New(deps Dependencies) (*Opener, error) {
	if deps.Keys == nil {
		return nil, fmt.Errorf("objectreader: %w: keys", errs.ErrMissingDependency)
	}
	source, err := NewStoredSource(SourceDependencies{
		Peers:     deps.Peers,
		Documents: deps.Documents,
		Stored:    deps.Stored,
		Clock:     deps.Clock,
	})
	if err != nil {
		return nil, err
	}
	lifetime, cancelLifetime := context.WithCancel(context.Background())
	return &Opener{
		source:           source,
		keys:             deps.Keys,
		lifetime:         lifetime,
		cancelLifetime:   cancelLifetime,
		encryptedReaders: make(map[*Reader]struct{}),
	}, nil
}

func (o *Opener) Close(ctx context.Context) error {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return nil
	}
	o.closed = true
	readers := make([]*Reader, 0, len(o.encryptedReaders))
	for reader := range o.encryptedReaders {
		readers = append(readers, reader)
	}
	clear(o.encryptedReaders)
	o.mu.Unlock()

	o.cancelLifetime()
	for _, active := range readers {
		active.closeFromOpener()
	}
	return o.source.Close(ctx)
}

func (o *Opener) Open(ctx context.Context, meta object.ObjectMeta) (Opened, error) {
	reader, err := o.open(ctx, meta)
	if err != nil {
		return nil, err
	}
	return reader, nil
}

func (o *Opener) open(ctx context.Context, meta object.ObjectMeta) (*Reader, error) {
	if o == nil {
		return nil, ErrClosed
	}
	if ctx == nil {
		return nil, ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	openCtx, cleanup, err := o.beginOpen(ctx)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	if err := validateMeta(meta); err != nil {
		return nil, err
	}
	reader := &Reader{meta: pinnedMeta(meta), source: o.source, openerDone: o.lifetime.Done()}
	if meta.BodyMsgID != 0 {
		if _, err := o.source.ResolveBody(openCtx, meta); err != nil {
			return nil, o.normalizeOpenError(err)
		}
	}
	if err := o.ensureOpen(); err != nil {
		return nil, err
	}
	if !meta.Encrypted {
		return reader, nil
	}
	if err := o.attachDecryptor(openCtx, reader); err != nil {
		return nil, err
	}
	return reader, nil
}

func (o *Opener) attachDecryptor(ctx context.Context, reader *Reader) error {
	dek, err := o.keys.DEK(ctx, reader.meta.ChannelID, reader.meta.KeyID)
	if dek != nil {
		defer dek.Clear()
	}
	if err != nil {
		return o.normalizeOpenError(fmt.Errorf("objectreader: %w: %w", errs.ErrKeyUnavailable, err))
	}
	if dek == nil || len(dek.Bytes()) == 0 {
		return fmt.Errorf("objectreader: %w: provider returned no key", errs.ErrKeyUnavailable)
	}
	if err := o.ensureOpen(); err != nil {
		return err
	}
	decryptor, err := randomaccess.NewRandomAccessDecryptor(ctx, storedView{source: reader.source, meta: reader.meta}, reader.meta.StoredSize, dek.Bytes())
	if err != nil {
		return o.normalizeOpenError(fmt.Errorf("objectreader: open encrypted stream: %w", err))
	}
	if decryptor.Size() != reader.meta.PlaintextSize {
		_ = decryptor.Close()
		return fmt.Errorf("objectreader: encrypted plaintext size %d does not match projection %d: %w", decryptor.Size(), reader.meta.PlaintextSize, randomaccess.ErrCiphertextSize)
	}
	if err := ctx.Err(); err != nil {
		_ = decryptor.Close()
		return o.normalizeOpenError(err)
	}
	reader.decryptor = decryptor
	if err := o.registerEncryptedReader(reader); err != nil {
		_ = decryptor.Close()
		return err
	}
	return nil
}

func (o *Opener) registerEncryptedReader(reader *Reader) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return ErrClosed
	}
	o.encryptedReaders[reader] = struct{}{}
	reader.owner = o
	return nil
}

func (o *Opener) releaseEncryptedReader(reader *Reader) {
	o.mu.Lock()
	delete(o.encryptedReaders, reader)
	o.mu.Unlock()
}

func (o *Opener) beginOpen(ctx context.Context) (context.Context, func(), error) {
	o.mu.RLock()
	closed := o.closed
	lifetime := o.lifetime
	o.mu.RUnlock()
	if closed {
		return nil, nil, ErrClosed
	}
	openCtx, cancel := context.WithCancel(ctx)
	stopLifetimeLink := context.AfterFunc(lifetime, cancel)
	if lifetime.Err() != nil {
		stopLifetimeLink()
		cancel()
		return nil, nil, ErrClosed
	}
	return openCtx, func() {
		stopLifetimeLink()
		cancel()
	}, nil
}

func (o *Opener) ensureOpen() error {
	o.mu.RLock()
	closed := o.closed
	o.mu.RUnlock()
	if closed {
		return ErrClosed
	}
	return nil
}

func (o *Opener) normalizeOpenError(err error) error {
	if o.ensureOpen() != nil {
		return ErrClosed
	}
	return err
}

type Reader struct {
	meta       object.ObjectMeta
	source     Source
	openerDone <-chan struct{}
	decryptor  *randomaccess.RandomAccessDecryptor
	owner      *Opener
	closed     atomic.Bool
}

func (r *Reader) Size() int64 {
	if r == nil {
		return 0
	}
	return r.meta.PlaintextSize
}

func (r *Reader) Close() error {
	if r == nil || !r.closed.CompareAndSwap(false, true) {
		return nil
	}
	var closeErr error
	if r.decryptor != nil {
		closeErr = r.decryptor.Close()
	}
	if r.owner != nil {
		r.owner.releaseEncryptedReader(r)
	}
	return closeErr
}

func (r *Reader) closeFromOpener() {
	if r != nil && r.decryptor != nil {
		_ = r.decryptor.Close()
	}
}

func (r *Reader) ReadAt(ctx context.Context, dst []byte, off int64) (int, error) {
	if err := r.validateRead(ctx, off); err != nil {
		return 0, err
	}
	if r.decryptor == nil {
		return r.source.ReadDocumentRange(ctx, r.meta, off, dst)
	}
	n, err := r.decryptor.ReadAt(ctx, dst, off)
	if errors.Is(err, randomaccess.ErrDecryptorClosed) {
		select {
		case <-r.openerDone:
			return n, ErrClosed
		default:
		}
		if r.closed.Load() {
			return n, ErrReaderClosed
		}
	}
	return n, err
}

func (r *Reader) WithContext(ctx context.Context) io.ReaderAt {
	return contextReaderAt{reader: r, ctx: ctx}
}

func (r *Reader) validateRead(ctx context.Context, off int64) error {
	if ctx == nil {
		return ErrNilContext
	}
	if r == nil || r.source == nil || r.closed.Load() {
		return ErrReaderClosed
	}
	select {
	case <-r.openerDone:
		return ErrClosed
	default:
	}
	if off < 0 {
		return ErrNegativeOffset
	}
	return ctx.Err()
}

type contextReaderAt struct {
	reader *Reader
	ctx    context.Context
}

func (c contextReaderAt) ReadAt(dst []byte, off int64) (int, error) {
	return c.reader.ReadAt(c.ctx, dst, off)
}

type storedView struct {
	source Source
	meta   object.ObjectMeta
}

func (v storedView) ReadAt(ctx context.Context, dst []byte, off int64) (int, error) {
	return v.source.ReadDocumentRange(ctx, v.meta, off, dst)
}

var _ randomaccess.ContextReaderAt = storedView{}
