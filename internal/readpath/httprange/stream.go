package httprange

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
)

const StreamChunkSize = 256 * 1024

type RangeSource interface {
	ReadAt(ctx context.Context, dst []byte, off int64) (int, error)
}

type Streamer struct {
	buffers sync.Pool
}

func NewStreamer() *Streamer {
	return &Streamer{buffers: sync.Pool{New: newChunkBuffer}}
}

func newChunkBuffer() any {
	buffer := make([]byte, StreamChunkSize)
	return &buffer
}

func (s *Streamer) Respond(ctx context.Context, w http.ResponseWriter, plan Plan, source RangeSource, headOnly bool) error {
	plan.SetHeaders(w.Header())
	w.WriteHeader(plan.Status)
	if headOnly || plan.Length == 0 {
		return nil
	}
	return s.Stream(ctx, w, source, plan.Range)
}

func (s *Streamer) Stream(ctx context.Context, w io.Writer, source RangeSource, selected ByteRange) error {
	pooled := s.buffers.Get().(*[]byte)
	defer s.buffers.Put(pooled)
	buffer := *pooled
	length := selected.Length()
	var sent int64
	for sent < length {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("httprange: stream: %w", err)
		}
		want := min(length-sent, int64(len(buffer)))
		n, err := source.ReadAt(ctx, buffer[:want], selected.Start+sent)
		if n > 0 {
			if _, writeErr := w.Write(buffer[:n]); writeErr != nil {
				return fmt.Errorf("httprange: write: %w", writeErr)
			}
			sent += int64(n)
		}
		if err != nil {
			if errors.Is(err, io.EOF) && sent == length {
				return nil
			}
			return fmt.Errorf("httprange: read: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("httprange: read: %w", io.ErrNoProgress)
		}
	}
	return nil
}
