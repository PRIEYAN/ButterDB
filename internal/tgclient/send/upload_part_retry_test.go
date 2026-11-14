package send

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

type partScript struct {
	mu         sync.Mutex
	calls      int
	firstError error
	failures   []error
	small      []*tg.UploadSaveFilePartRequest
	big        []*tg.UploadSaveBigFilePartRequest
}

func (s *partScript) handle(_ context.Context, input bin.Encoder, output bin.Decoder) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	switch request := input.(type) {
	case *tg.UploadSaveFilePartRequest:
		s.small = append(s.small, request)
	case *tg.UploadSaveBigFilePartRequest:
		s.big = append(s.big, request)
	}
	if s.calls <= len(s.failures) {
		return s.failures[s.calls-1]
	}
	if s.calls == 1 && s.firstError != nil {
		return s.firstError
	}
	output.(*tg.BoolBox).Bool = &tg.BoolTrue{}
	return nil
}

func newPartClient(handle func(context.Context, bin.Encoder, bin.Decoder) error, slots UploadSlots, retry RetryPolicy) (*retryingPartClient, *apiInvoker) {
	invoker := newAPIInvoker(handle)
	return &retryingPartClient{invoker: invoker, slots: slots, retry: retry}, invoker
}

func TestRetryingUploadClientRetainsPartThroughRepeatedThrottling(t *testing.T) {
	for _, big := range []bool{false, true} {
		name := "small"
		if big {
			name = "big"
		}
		t.Run(name, func(t *testing.T) {
			waits := []time.Duration{16 * time.Second, 15 * time.Second, time.Minute, 16 * time.Second}
			script := &partScript{}
			for _, wait := range waits {
				script.failures = append(script.failures, tgerr.New(420, "FLOOD_WAIT_"+strconv.Itoa(int(wait/time.Second))))
			}
			retry := newScriptedRetry()
			client, _ := newPartClient(script.handle, newSlotPool(UploadThreads), retry)
			body := []byte("retained upload chunk")
			var accepted bool
			var err error
			if big {
				request := &tg.UploadSaveBigFilePartRequest{FileID: 97, FilePart: 12, FileTotalParts: 20, Bytes: body}
				accepted, err = client.UploadSaveBigFilePart(t.Context(), request)
				for _, attempt := range script.big {
					if attempt != request || attempt.FileID != 97 || attempt.FilePart != 12 || attempt.FileTotalParts != 20 || !bytes.Equal(attempt.Bytes, body) {
						t.Fatalf("retried big request = %+v, want original body and identity", attempt)
					}
				}
			} else {
				request := &tg.UploadSaveFilePartRequest{FileID: 97, FilePart: 2, Bytes: body}
				accepted, err = client.UploadSaveFilePart(t.Context(), request)
				for _, attempt := range script.small {
					if attempt != request || attempt.FileID != 97 || attempt.FilePart != 2 || !bytes.Equal(attempt.Bytes, body) {
						t.Fatalf("retried small request = %+v, want original body and identity", attempt)
					}
				}
			}
			if err != nil || !accepted || script.calls != 5 || !slices.Equal(retry.slept, waits) {
				t.Fatalf("part = %v, %v after %d calls and waits %v; want acceptance after all cooldowns", accepted, err, script.calls, retry.slept)
			}
		})
	}
}

func TestRetryingUploadClientReleasesSlotDuringCancelableCooldown(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	script := &partScript{firstError: tgerr.New(420, "FLOOD_WAIT_3600")}
	slots := newSlotPool(UploadThreads)
	retry := newScriptedRetry()
	retry.sleep = func(ctx context.Context, _ time.Duration) error {
		slotCtx, stop := context.WithTimeout(ctx, time.Second)
		defer stop()
		var releases []func()
		defer func() {
			for _, release := range releases {
				release()
			}
		}()
		for range UploadThreads {
			release, err := slots.Acquire(slotCtx)
			if err != nil {
				t.Fatalf("upload retained a slot during flood wait: %v", err)
			}
			releases = append(releases, release)
		}
		cancel()
		return ctx.Err()
	}
	client, _ := newPartClient(script.handle, slots, retry)
	_, err := client.UploadSaveFilePart(ctx, &tg.UploadSaveFilePartRequest{})
	if !errors.Is(err, context.Canceled) || script.calls != 1 {
		t.Fatalf("upload part = %v after %d calls, want canceled after one", err, script.calls)
	}
	if slots.free() != UploadThreads {
		t.Fatalf("free slots = %d, want %d", slots.free(), UploadThreads)
	}
}

func TestRetryingUploadClientReacquiresAndRetriesSameTelegramPart(t *testing.T) {
	tests := []struct {
		name string
		call func(*retryingPartClient) (bool, error)
	}{
		{name: "small part", call: func(c *retryingPartClient) (bool, error) {
			return c.UploadSaveFilePart(context.Background(), &tg.UploadSaveFilePartRequest{FileID: 77, FilePart: 705, Bytes: []byte("same buffered chunk")})
		}},
		{name: "big part", call: func(c *retryingPartClient) (bool, error) {
			return c.UploadSaveBigFilePart(context.Background(), &tg.UploadSaveBigFilePartRequest{FileID: 77, FilePart: 705, FileTotalParts: 3800, Bytes: []byte("same buffered chunk")})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			script := &partScript{}
			for range 8 {
				script.failures = append(script.failures, errors.New("engine forcibly closed: context canceled"))
			}
			client, invoker := newPartClient(script.handle, newSlotPool(UploadThreads), newScriptedRetry())
			ok, err := test.call(client)
			if err != nil || !ok {
				t.Fatalf("upload part = (%v, %v), want (true, nil)", ok, err)
			}
			if script.calls != 9 || invoker.scopes != 9 {
				t.Fatalf("part calls=%d scope acquires=%d, want 9 and 9", script.calls, invoker.scopes)
			}
			if len(script.small)+len(script.big) != 9 {
				t.Fatalf("requests = %d, want 9", len(script.small)+len(script.big))
			}
			for _, request := range script.small {
				if request != script.small[0] || request.FileID != 77 || request.FilePart != 705 {
					t.Fatalf("small-part retry = %+v, want original request", request)
				}
			}
			for _, request := range script.big {
				if request != script.big[0] || request.FileID != 77 || request.FilePart != 705 || request.FileTotalParts != 3800 {
					t.Fatalf("big-part retry = %+v, want original request", request)
				}
			}
		})
	}
}

func TestRetryingUploadClientDoesNotRetryPermanentRPCError(t *testing.T) {
	want := tgerr.New(400, "FILE_PARTS_INVALID")
	script := &partScript{firstError: want}
	client, _ := newPartClient(script.handle, newSlotPool(UploadThreads), newScriptedRetry())
	_, err := client.UploadSaveBigFilePart(context.Background(), &tg.UploadSaveBigFilePartRequest{})
	if !tgerr.Is(err, "FILE_PARTS_INVALID") || script.calls != 1 {
		t.Fatalf("error=%v calls=%d, want permanent error after one call", err, script.calls)
	}
}

func TestRetryingUploadClientSharesOnePartBudget(t *testing.T) {
	ctx := context.Background()
	slots := newSlotPool(UploadThreads)
	var held []func()
	for range UploadThreads {
		release, err := slots.Acquire(ctx)
		if err != nil {
			t.Fatalf("acquire budget: %v", err)
		}
		held = append(held, release)
	}
	sent := make(chan struct{}, 1)
	script := &partScript{}
	client, _ := newPartClient(func(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
		sent <- struct{}{}
		return script.handle(ctx, input, output)
	}, slots, newScriptedRetry())
	done := make(chan error, 1)
	go func() {
		_, err := client.UploadSaveBigFilePart(ctx, &tg.UploadSaveBigFilePartRequest{FilePart: 3})
		done <- err
	}()

	select {
	case <-sent:
		t.Fatal("a part was sent while the upload budget was spent")
	case <-time.After(30 * time.Millisecond):
	}

	held[0]()
	held = held[1:]
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("part after a slot freed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("freed slot did not admit the waiting part")
	}
	for _, release := range held {
		release()
	}
	if script.calls != 1 {
		t.Fatalf("parts sent = %d, want exactly one", script.calls)
	}
}
