package send

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

var testPeer = tgclient.InputPeer{ChannelID: 100, AccessHash: 200}

type handlerInvoker struct {
	handle func(ctx context.Context, input bin.Encoder, output bin.Decoder) error
}

func (h handlerInvoker) Invoke(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
	return h.handle(ctx, input, output)
}

type apiInvoker struct {
	api    *tg.Client
	scopes int
}

func newAPIInvoker(handle func(ctx context.Context, input bin.Encoder, output bin.Decoder) error) *apiInvoker {
	return &apiInvoker{api: tg.NewClient(handlerInvoker{handle: handle})}
}

func (a *apiInvoker) Invoke(ctx context.Context, fn func(ctx context.Context, api *tg.Client) error) error {
	a.scopes++
	return fn(ctx, a.api)
}

func (a *apiInvoker) InvokeMedia(ctx context.Context, _ int, fn func(ctx context.Context, api *tg.Client) error) error {
	return fn(ctx, a.api)
}

type recordingGate struct {
	mu      sync.Mutex
	classes []tgclient.WriteClass
	inside  int
	maxHeld int
}

func (g *recordingGate) Do(ctx context.Context, class tgclient.WriteClass, action func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	g.mu.Lock()
	g.classes = append(g.classes, class)
	g.inside++
	g.maxHeld = max(g.maxHeld, g.inside)
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		g.inside--
		g.mu.Unlock()
	}()
	return action()
}

func (g *recordingGate) recorded() []tgclient.WriteClass {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]tgclient.WriteClass(nil), g.classes...)
}

type slotPool struct {
	tokens chan struct{}
}

func newSlotPool(n int) *slotPool {
	pool := &slotPool{tokens: make(chan struct{}, n)}
	for range n {
		pool.tokens <- struct{}{}
	}
	return pool
}

func (p *slotPool) Acquire(ctx context.Context) (func(), error) {
	select {
	case <-p.tokens:
		return func() { p.tokens <- struct{}{} }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *slotPool) free() int {
	return len(p.tokens)
}

type scriptedRetry struct {
	sleep    func(ctx context.Context, wait time.Duration) error
	maxTries int
	slept    []time.Duration
}

func newScriptedRetry() *scriptedRetry {
	return &scriptedRetry{maxTries: 50}
}

func (r *scriptedRetry) Do(ctx context.Context, action func() error) error {
	for attempt := 1; ; attempt++ {
		err := action()
		if err == nil {
			return nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return errors.Join(ctxErr, err)
		}
		var flood *tgclient.FloodWaitError
		switch {
		case errors.As(err, &flood):
			r.slept = append(r.slept, flood.Duration)
			if r.sleep != nil {
				if sleepErr := r.sleep(ctx, flood.Duration); sleepErr != nil {
					return errors.Join(sleepErr, err)
				}
			}
		case isTransientForTest(err) && attempt < r.maxTries:
		default:
			return err
		}
	}
}

func isTransientForTest(err error) bool {
	text := err.Error()
	return strings.Contains(text, "connection reset") || strings.Contains(text, "engine forcibly closed")
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type harness struct {
	invoker *apiInvoker
	gate    *recordingGate
	slots   *slotPool
	retry   *scriptedRetry
	sender  *Sender
}

func newHarness(t *testing.T, handle func(ctx context.Context, input bin.Encoder, output bin.Decoder) error) *harness {
	t.Helper()
	h := &harness{
		invoker: newAPIInvoker(handle),
		gate:    &recordingGate{},
		slots:   newSlotPool(UploadThreads),
		retry:   newScriptedRetry(),
	}
	sender, err := New(Dependencies{
		Invoker:      h.invoker,
		Writes:       h.gate,
		Slots:        h.slots,
		UploadRetry:  h.retry,
		ControlRetry: h.retry,
		Logger:       discardLogger(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.sender = sender
	return h
}

type fakeTelegram struct {
	mu       sync.Mutex
	nextID   int
	byRandom map[int64]int
	sent     []any
	parts    int
	files    map[int64]*tg.InputFile
}

func newFakeTelegram() *fakeTelegram {
	return &fakeTelegram{nextID: 100, byRandom: map[int64]int{}}
}

func (f *fakeTelegram) handle(_ context.Context, input bin.Encoder, output bin.Decoder) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch request := input.(type) {
	case *tg.MessagesSendMessageRequest:
		output.(*tg.UpdatesBox).Updates = f.accept(request.RandomID, request)
	case *tg.MessagesSendMediaRequest:
		output.(*tg.UpdatesBox).Updates = f.accept(request.RandomID, request)
	case *tg.UploadSaveFilePartRequest:
		f.parts++
		output.(*tg.BoolBox).Bool = &tg.BoolTrue{}
	case *tg.UploadSaveBigFilePartRequest:
		f.parts++
		output.(*tg.BoolBox).Bool = &tg.BoolTrue{}
	default:
		return errors.New("unexpected request")
	}
	return nil
}

func (f *fakeTelegram) accept(randomID int64, request any) tg.UpdatesClass {
	id, seen := f.byRandom[randomID]
	if !seen {
		f.nextID++
		id = f.nextID
		f.byRandom[randomID] = id
		f.sent = append(f.sent, request)
	}
	return &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateMessageID{ID: id, RandomID: randomID}}}
}

func (f *fakeTelegram) sentCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}
