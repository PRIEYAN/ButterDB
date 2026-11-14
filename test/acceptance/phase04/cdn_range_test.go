package phase04

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"errors"
	"strings"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/core"
	"github.com/PRIEYAN/ButterDB/internal/platform/config"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/faketg"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/rangeio"
)

const documentBytes = 2*1024*1024 + 12345

var errCDNHashMismatch = errors.New("rangeio: cdn integrity: hash mismatch at 0")

func seedDocument(t *testing.T, w *world) (tgclient.DocumentRef, []byte) {
	t.Helper()
	data := patternedBytes(documentBytes)
	msgID, err := w.network.SeedDocument(channelID, "body.bin", "TDX1|t=body|j=job-1", data)
	if err != nil {
		t.Fatalf("seed document: %v", err)
	}
	ref, err := w.client.ResolveDocument(context.Background(), w.peer, msgID)
	if err != nil {
		t.Fatalf("resolve document: %v", err)
	}
	return ref, data
}

func newStream(t *testing.T, w *world) *rangeio.Stream {
	t.Helper()
	control, err := core.NewFlowControl(core.FlowDependencies{
		SendRate: config.Default().Telegram.SendRate,
		Clock:    w.clk,
		Sleeper:  &recordingSleeper{},
		Random:   cryptorand.Reader,
		Logger:   discardLogger(),
	})
	if err != nil {
		t.Fatalf("NewFlowControl: %v", err)
	}
	stream, err := rangeio.NewStream(rangeio.StreamDependencies{Blocks: w.client, Resolver: w.client, Retry: control.BlockRetry})
	if err != nil {
		t.Fatalf("NewStream: %v", err)
	}
	return stream
}

func TestRangeReadsVerifyCDNHashes(t *testing.T) {
	t.Run("a verified cdn redirect delivers the exact bytes", func(t *testing.T) {
		w := newWorld(t)
		ref, data := seedDocument(t, w)
		if err := w.client.FaultOnCall(faketg.MethodReadDocumentRange, 2, faketg.Fault{Kind: faketg.FaultCDNRedirect}); err != nil {
			t.Fatalf("fault: %v", err)
		}
		var out bytes.Buffer
		if err := newStream(t, w).Copy(context.Background(), ref, &out); err != nil {
			t.Fatalf("Copy: %v", err)
		}
		if !bytes.Equal(out.Bytes(), data) {
			t.Fatal("streamed bytes differ from the stored document")
		}
		if got := w.client.CDNRedirectCount(); got != 1 {
			t.Fatalf("cdn redirects served = %d, want 1", got)
		}
	})

	t.Run("a hash mismatch is never written to the destination", func(t *testing.T) {
		w := newWorld(t)
		ref, data := seedDocument(t, w)
		fault := faketg.Fault{Kind: faketg.FaultCDNRedirect, Err: errCDNHashMismatch}
		if err := w.client.FaultOnCall(faketg.MethodReadDocumentRange, 2, fault); err != nil {
			t.Fatalf("fault: %v", err)
		}
		var out bytes.Buffer
		err := newStream(t, w).Copy(context.Background(), ref, &out)
		if !errors.Is(err, errCDNHashMismatch) || !strings.Contains(err.Error(), "download block at") {
			t.Fatalf("Copy = %v, want the integrity failure naming the block", err)
		}
		if int64(out.Len()) != rangeio.StreamBlockBytes || !bytes.Equal(out.Bytes(), data[:rangeio.StreamBlockBytes]) {
			t.Fatalf("destination holds %d bytes, want only the first verified block of %d", out.Len(), rangeio.StreamBlockBytes)
		}
		if got := w.client.CallCount(faketg.MethodReadDocumentRange); got != 2 {
			t.Fatalf("read calls = %d, want 2: an integrity failure must not be retried", got)
		}
	})

	t.Run("an expired file reference is refreshed once and the read continues", func(t *testing.T) {
		w := newWorld(t)
		ref, data := seedDocument(t, w)
		if err := w.client.FaultOnCall(faketg.MethodReadDocumentRange, 2, faketg.Fault{Kind: faketg.FaultExpireFileReference}); err != nil {
			t.Fatalf("fault: %v", err)
		}
		var out bytes.Buffer
		if err := newStream(t, w).Copy(context.Background(), ref, &out); err != nil {
			t.Fatalf("Copy: %v", err)
		}
		if !bytes.Equal(out.Bytes(), data) {
			t.Fatal("streamed bytes differ after the reference refresh")
		}
		if got := w.client.CallCount(faketg.MethodResolveDocument); got != 2 {
			t.Fatalf("resolve calls = %d, want 2 (the test's own plus exactly one refresh)", got)
		}
	})
}
