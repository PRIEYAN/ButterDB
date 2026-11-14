package core

import (
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
)

const probeSeedBytes = 32

var errProbeBodySeek = errors.New("core: probe body only rewinds or reports its size")

type probeBody struct {
	seed [probeSeedBytes]byte
	size int64
	pos  int64
	gen  *rand.ChaCha8
}

func newProbeBody(random io.Reader, size int64) (*probeBody, error) {
	body := &probeBody{size: size}
	if _, err := io.ReadFull(random, body.seed[:]); err != nil {
		return nil, fmt.Errorf("core: probe body seed: %w", err)
	}
	body.gen = rand.NewChaCha8(body.seed)
	return body, nil
}

func (b *probeBody) Read(p []byte) (int, error) {
	if b.pos >= b.size {
		return 0, io.EOF
	}
	want := p[:min(int64(len(p)), b.size-b.pos)]
	n, _ := b.gen.Read(want)
	b.pos += int64(n)
	return n, nil
}

func (b *probeBody) Seek(offset int64, whence int) (int64, error) {
	switch {
	case whence == io.SeekStart && offset == 0:
		b.gen = rand.NewChaCha8(b.seed)
		b.pos = 0
		return 0, nil
	case whence == io.SeekEnd && offset == 0:
		return b.size, nil
	case whence == io.SeekCurrent && offset == 0:
		return b.pos, nil
	}
	return 0, errProbeBodySeek
}
