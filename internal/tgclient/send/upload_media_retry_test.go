package send

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

func publishMedia(h *harness, ctx context.Context, request *tg.MessagesSendMediaRequest) (receipt, error) {
	return h.sender.publish(ctx, h.retry, tgclient.WriteDocument, request.RandomID, "send file",
		func(ctx context.Context, api *tg.Client) (tg.UpdatesClass, error) {
			return api.MessagesSendMedia(ctx, request)
		})
}

func TestSendUploadedMediaRetainsUploadedHandleAndIdentityThroughThrottling(t *testing.T) {
	file := &tg.InputFileBig{ID: 87, Parts: 20, Name: "clip.mkv"}
	request := &tg.MessagesSendMediaRequest{RandomID: 53, Message: "file header", Media: &tg.InputMediaUploadedDocument{File: file}}
	waits := []time.Duration{16 * time.Second, 15 * time.Second, time.Minute, 16 * time.Second}
	attempts := 0
	h := newHarness(t, func(_ context.Context, input bin.Encoder, output bin.Decoder) error {
		actual := input.(*tg.MessagesSendMediaRequest)
		if actual != request || actual.RandomID != 53 || actual.Media.(*tg.InputMediaUploadedDocument).File != file {
			t.Fatalf("media request = %+v, want original uploaded handle and random id", actual)
		}
		attempts++
		if attempts <= len(waits) {
			return tgerr.New(420, "FLOOD_WAIT_"+itoaSeconds(waits[attempts-1]))
		}
		output.(*tg.UpdatesBox).Updates = &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateMessageID{ID: 99, RandomID: 53}}}
		return nil
	})
	sent, err := publishMedia(h, t.Context(), request)
	if err != nil || sent.msgID != 99 || attempts != 5 {
		t.Fatalf("send = %+v, %v after %d attempts; want receipt 99 after all cooldowns", sent, err, attempts)
	}
	if len(h.retry.slept) != len(waits) {
		t.Fatalf("cooldowns honoured = %v, want %v", h.retry.slept, waits)
	}
}

func TestSendUploadedMediaRecoversLostReceiptWithoutDuplicate(t *testing.T) {
	file := &tg.InputFileBig{ID: 87, Parts: 20, Name: "clip.mkv"}
	request := &tg.MessagesSendMediaRequest{RandomID: 53, Media: &tg.InputMediaUploadedDocument{File: file}}
	accepted := map[int64]int{}
	attempts := 0
	h := newHarness(t, func(_ context.Context, input bin.Encoder, output bin.Decoder) error {
		actual := input.(*tg.MessagesSendMediaRequest)
		attempts++
		if actual != request || actual.Media.(*tg.InputMediaUploadedDocument).File != file {
			t.Fatal("retry changed the publication identity or uploaded handle")
		}
		if _, exists := accepted[actual.RandomID]; !exists {
			accepted[actual.RandomID] = 99
		}
		if attempts <= 8 {
			return errors.New("connection reset by peer")
		}
		output.(*tg.UpdatesBox).Updates = &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateMessageID{ID: accepted[actual.RandomID], RandomID: actual.RandomID}}}
		return nil
	})
	sent, err := publishMedia(h, t.Context(), request)
	if err != nil || sent.msgID != 99 || attempts != 9 || len(accepted) != 1 {
		t.Fatalf("send = %+v, %v; attempts=%d accepted=%v", sent, err, attempts, accepted)
	}
}

func TestSendUploadedMediaRetainsUnknownOutcomeWhenRecoveryStops(t *testing.T) {
	for _, cancelRecovery := range []bool{false, true} {
		name := "recovery fails permanently"
		if cancelRecovery {
			name = "recovery canceled"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			attempts := 0
			transport := errors.New("connection reset by peer")
			permanent := tgerr.New(400, "FILE_PART_MISSING")
			h := newHarness(t, func(context.Context, bin.Encoder, bin.Decoder) error {
				attempts++
				if attempts == 1 {
					if cancelRecovery {
						cancel()
					}
					return transport
				}
				return permanent
			})
			_, err := publishMedia(h, ctx, &tg.MessagesSendMediaRequest{RandomID: 53})
			want := error(permanent)
			if cancelRecovery {
				want = context.Canceled
			}
			if !errors.Is(err, want) && !(!cancelRecovery && tgerr.Is(err, "FILE_PART_MISSING")) {
				t.Fatalf("send = %v, want terminal error %v", err, want)
			}
			if !errors.Is(err, tgclient.ErrSendOutcomeUnknown) || !errors.Is(err, transport) {
				t.Fatalf("send = %v, want retained unknown outcome of the first attempt", err)
			}
			if h.gate.inside != 0 {
				t.Fatal("recovery retained the write gate")
			}
		})
	}
}

func TestSendUploadedMediaCancellationReleasesWriteGate(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	attempts := 0
	h := newHarness(t, func(context.Context, bin.Encoder, bin.Decoder) error {
		attempts++
		return tgerr.New(420, "FLOOD_WAIT_3600")
	})
	h.retry.sleep = func(ctx context.Context, _ time.Duration) error {
		cancel()
		return ctx.Err()
	}
	_, err := publishMedia(h, ctx, &tg.MessagesSendMediaRequest{RandomID: 53})
	if !errors.Is(err, context.Canceled) || attempts != 1 {
		t.Fatalf("send = %v after %d attempts, want canceled during first cooldown", err, attempts)
	}
	if h.gate.inside != 0 {
		t.Fatal("canceled media publication retained the write gate")
	}
}

func itoaSeconds(wait time.Duration) string {
	return strconv.Itoa(int(wait / time.Second))
}
