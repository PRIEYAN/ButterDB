package phase10

import (
	"context"
	"errors"
	"testing"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
)

func TestGUILinkFlowServesBucketsWithoutRestart(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	owner := w.claim()

	status, err := owner.Status(ctx)
	if err != nil || status.State != "unlinked" {
		t.Fatalf("status before linking = %+v, %v", status, err)
	}
	if _, err := owner.SaveTelegramCredentials(ctx, adminv1.TelegramCredentialsRequest{APIID: 1234567, APIHash: telegramHash}); err != nil {
		t.Fatalf("save credentials: %v", err)
	}
	w.linkByPhoneAndCode(owner)

	status = w.awaitReady(owner)
	if !status.Telegram.Linked || !status.Telegram.Connected {
		t.Fatalf("ready status = %+v, want linked and connected", status)
	}
	if !hasComponent(status, "registry", "verified") {
		t.Fatalf("components = %+v, want registry verified", status.Components)
	}

	created, err := owner.CreateBucket(ctx, adminv1.CreateBucketRequest{Name: "photos"})
	if err != nil || created.Name != "photos" {
		t.Fatalf("CreateBucket = %+v, %v", created, err)
	}
	list, err := owner.ListBuckets(ctx)
	if err != nil || len(list.Buckets) != 1 || list.Buckets[0].Name != "photos" {
		t.Fatalf("ListBuckets = %+v, %v", list, err)
	}
	usage, err := owner.Usage(ctx)
	if err != nil || usage.Buckets != 1 {
		t.Fatalf("Usage = %+v, %v", usage, err)
	}
}

func TestUnlinkThenRelinkRestoresStorage(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	owner := w.claim()
	if _, err := owner.SaveTelegramCredentials(ctx, adminv1.TelegramCredentialsRequest{APIID: 1234567, APIHash: telegramHash}); err != nil {
		t.Fatalf("save credentials: %v", err)
	}
	w.linkByPhoneAndCode(owner)
	w.awaitReady(owner)
	if _, err := owner.CreateBucket(ctx, adminv1.CreateBucketRequest{Name: "archive"}); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}

	unlinked, err := owner.UnlinkTelegram(ctx)
	if err != nil || unlinked.State == adminv1.TelegramLinked {
		t.Fatalf("UnlinkTelegram = %+v, %v", unlinked, err)
	}
	_, err = owner.ListBuckets(ctx)
	requireCode(t, err, adminv1.CodeTelegramNotLinked)

	w.linkByPhoneAndCode(owner)
	w.awaitReady(owner)
	list, err := owner.ListBuckets(ctx)
	if err != nil || len(list.Buckets) != 1 || list.Buckets[0].Name != "archive" {
		t.Fatalf("ListBuckets after relink = %+v, %v", list, err)
	}
	if _, err := owner.CreateBucket(ctx, adminv1.CreateBucketRequest{Name: "photos"}); err != nil {
		t.Fatalf("CreateBucket after relink: %v", err)
	}
	if got := len(w.network.ChannelIDs()); got != 3 {
		t.Fatalf("channels = %d, want the registry and two buckets", got)
	}
}

func hasComponent(status adminv1.Status, name, want string) bool {
	for _, component := range status.Components {
		if component.Name == name {
			return component.Status == want
		}
	}
	return false
}

func requireCode(t *testing.T, err error, want adminv1.Code) {
	t.Helper()
	var apiErr *adminv1.APIError
	if !errors.As(err, &apiErr) || apiErr.Body.Code != want {
		t.Fatalf("error = %v, want an *adminv1.APIError with code %s", err, want)
	}
}
