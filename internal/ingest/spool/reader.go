package spool

import (
	"context"
	"fmt"
	"io"
)

const maxEmptyReads = 100

type exactLengthReader struct {
	ctx        context.Context
	source     io.Reader
	remaining  int64
	emptyReads int
}

func (r *exactLengthReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, fmt.Errorf("spool: read body: %w", err)
	}
	if len(buffer) == 0 {
		return 0, nil
	}
	if r.remaining == 0 {
		return r.probeTrailingData()
	}
	if int64(len(buffer)) > r.remaining {
		buffer = buffer[:r.remaining]
	}
	readCount, err := r.source.Read(buffer)
	if readCount < 0 || readCount > len(buffer) {
		return 0, ErrLengthMismatch
	}
	r.remaining -= int64(readCount)
	return r.finishRead(readCount, err)
}

func (r *exactLengthReader) finishRead(readCount int, err error) (int, error) {
	if err == io.EOF {
		if r.remaining != 0 {
			return readCount, ErrLengthMismatch
		}
		return readCount, io.EOF
	}
	if err != nil {
		return readCount, err
	}
	if readCount > 0 {
		r.emptyReads = 0
		return readCount, nil
	}
	return 0, r.noteEmptyRead()
}

func (r *exactLengthReader) probeTrailingData() (int, error) {
	var extra [1]byte
	readCount, err := r.source.Read(extra[:])
	if readCount > 0 {
		return 0, ErrLengthMismatch
	}
	if err != nil {
		return 0, err
	}
	return 0, r.noteEmptyRead()
}

func (r *exactLengthReader) noteEmptyRead() error {
	r.emptyReads++
	if r.emptyReads >= maxEmptyReads {
		return io.ErrNoProgress
	}
	return nil
}
