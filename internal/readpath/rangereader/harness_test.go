package rangereader

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/readpath"
)

const alignmentBytes int64 = 4 * 1024

type rangeClient interface {
	readpath.DocumentRangeReader
	DocumentResolver
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

var _ io.Reader = zeroReader{}

var immediateSleeper = SleeperFunc(func(ctx context.Context, _ time.Duration) error {
	return ctx.Err()
})

func startReader(t testing.TB, client rangeClient, cfg RangeReaderConfig) *RangeReader {
	return startReaderSleeping(t, client, cfg, immediateSleeper)
}

func startReaderSleeping(t testing.TB, client rangeClient, cfg RangeReaderConfig, sleeper Sleeper) *RangeReader {
	t.Helper()
	reader, err := NewRangeReader(Dependencies{
		Client:   client,
		Resolver: client,
		Clock:    clock.System{},
		Sleeper:  sleeper,
		Random:   zeroReader{},
		Logger:   slog.New(slog.DiscardHandler),
	}, cfg)
	if err != nil {
		t.Fatalf("NewRangeReader: %v", err)
	}
	if err := reader.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := reader.Close(closeCtx); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return reader
}
