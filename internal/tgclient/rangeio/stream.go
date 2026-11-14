package rangeio

import (
	"context"
	"fmt"
	"io"
	"sync/atomic"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

const StreamBlockBytes = int64(RangeReadMaxBytes)

type BlockReader interface {
	ReadDocumentRange(ctx context.Context, ref tgclient.DocumentRef, offset int64, dst []byte) (int, error)
}

type DocumentResolver interface {
	ResolveDocument(ctx context.Context, peer tgclient.InputPeer, msgID int64) (tgclient.DocumentRef, error)
}

type BlockRetry interface {
	Do(ctx context.Context, action func() error) error
}

type StreamDependencies struct {
	Blocks   BlockReader
	Resolver DocumentResolver
	Retry    BlockRetry
}

type Stream struct {
	blocks   BlockReader
	resolver DocumentResolver
	retry    BlockRetry
	retries  atomic.Int64
}

func NewStream(deps StreamDependencies) (*Stream, error) {
	switch {
	case deps.Blocks == nil:
		return nil, fmt.Errorf("rangeio: %w: blocks", errs.ErrMissingDependency)
	case deps.Resolver == nil:
		return nil, fmt.Errorf("rangeio: %w: resolver", errs.ErrMissingDependency)
	case deps.Retry == nil:
		return nil, fmt.Errorf("rangeio: %w: retry", errs.ErrMissingDependency)
	}
	return &Stream{blocks: deps.Blocks, resolver: deps.Resolver, retry: deps.Retry}, nil
}

func (s *Stream) Retries() int64 {
	return s.retries.Load()
}

// Each block retries in place, so a dropped connection costs one block and never the bytes already written.
func (s *Stream) Copy(ctx context.Context, ref tgclient.DocumentRef, w io.Writer) error {
	download := &documentDownload{stream: s, ref: ref}
	buf := make([]byte, StreamBlockBytes)
	for offset := int64(0); offset < ref.Size; offset += StreamBlockBytes {
		want := buf[:min(StreamBlockBytes, ref.Size-offset)]
		if err := download.readBlock(ctx, offset, want); err != nil {
			return err
		}
		if _, err := w.Write(want); err != nil {
			return fmt.Errorf("rangeio: write block at %d: %w", offset, err)
		}
	}
	return nil
}

type documentDownload struct {
	stream *Stream
	ref    tgclient.DocumentRef
}

// A stale file reference is refreshed once and the read repeated within the same attempt; it is not a retry.
func (d *documentDownload) readBlock(ctx context.Context, offset int64, dst []byte) error {
	attempts := 0
	err := d.stream.retry.Do(ctx, func() error {
		attempts++
		ref := d.ref
		_, err := d.stream.blocks.ReadDocumentRange(ctx, ref, offset, dst)
		if IsFileReferenceError(err) {
			if ref, err = d.refreshRef(ctx, ref); err == nil {
				_, err = d.stream.blocks.ReadDocumentRange(ctx, ref, offset, dst)
			}
		}
		if err != nil {
			return fmt.Errorf("rangeio: download block at %d: %w", offset, normalizeError(err))
		}
		return nil
	})
	if attempts > 1 {
		d.stream.retries.Add(int64(attempts - 1))
	}
	return err
}

func (d *documentDownload) refreshRef(ctx context.Context, stale tgclient.DocumentRef) (tgclient.DocumentRef, error) {
	ref, err := d.stream.resolver.ResolveDocument(ctx, stale.Peer, stale.MsgID)
	if err != nil {
		return tgclient.DocumentRef{}, err
	}
	d.ref = ref
	return ref, nil
}
