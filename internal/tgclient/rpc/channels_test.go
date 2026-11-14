package rpc

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram/query/dialogs"
	"github.com/gotd/td/tg"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

type scriptedDialogsQuery struct {
	responses []tg.MessagesDialogsClass
	errAt     int
	err       error
	calls     []dialogs.Request
}

func (q *scriptedDialogsQuery) Query(_ context.Context, request dialogs.Request) (tg.MessagesDialogsClass, error) {
	q.calls = append(q.calls, request)
	call := len(q.calls)
	if q.err != nil && call == q.errAt {
		return nil, q.err
	}
	if call <= len(q.responses) {
		return q.responses[call-1], nil
	}
	return &tg.MessagesDialogsSlice{}, nil
}

type dialogChannel struct {
	id         int64
	title      string
	creator    bool
	broadcast  bool
	megagroup  bool
	left       bool
	topMessage int
}

func dialogsPage(rows ...dialogChannel) tg.MessagesDialogsClass {
	result := &tg.MessagesDialogsSlice{Count: len(rows)}
	for _, row := range rows {
		peer := &tg.PeerChannel{ChannelID: row.id}
		result.Dialogs = append(result.Dialogs, &tg.Dialog{Peer: peer, TopMessage: row.topMessage})
		result.Chats = append(result.Chats, &tg.Channel{
			ID: row.id, AccessHash: row.id + 1000, Title: row.title, Creator: row.creator,
			Broadcast: row.broadcast, Megagroup: row.megagroup, Left: row.left, Photo: &tg.ChatPhotoEmpty{},
		})
		if row.topMessage > 0 {
			result.Messages = append(result.Messages, &tg.Message{ID: row.topMessage, PeerID: peer, Date: 10})
		}
	}
	return result
}

func TestCollectOwnedChannelsPaginatesFiltersAndDeduplicates(t *testing.T) {
	main := &scriptedDialogsQuery{responses: []tg.MessagesDialogsClass{
		dialogsPage(
			dialogChannel{id: 1001, title: "bucket-a", creator: true, broadcast: true, topMessage: 90},
			dialogChannel{id: 2001, title: "Joined", broadcast: true, topMessage: 8},
			dialogChannel{id: 2002, title: "Group", creator: true, megagroup: true, topMessage: 8},
		),
		dialogsPage(
			dialogChannel{id: 1003, title: " Archive ", creator: true, broadcast: true, topMessage: 7},
			dialogChannel{id: 2003, title: "Left", creator: true, broadcast: true, left: true, topMessage: 8},
		),
		&tg.MessagesDialogsSlice{},
	}}
	archive := &scriptedDialogsQuery{responses: []tg.MessagesDialogsClass{
		dialogsPage(
			dialogChannel{id: 1001, title: "bucket-a", creator: true, broadcast: true, topMessage: 90},
			dialogChannel{id: 1002, title: "bucket-b", creator: true, broadcast: true},
		),
		&tg.MessagesDialogsSlice{},
	}}

	got, err := collectOwnedChannels(t.Context(), main, archive)
	if err != nil {
		t.Fatal(err)
	}
	want := []tgclient.Channel{
		{Peer: tgclient.InputPeer{ChannelID: 1001, AccessHash: 2001}, Title: "bucket-a"},
		{Peer: tgclient.InputPeer{ChannelID: 1003, AccessHash: 2003}, Title: "Archive"},
		{Peer: tgclient.InputPeer{ChannelID: 1002, AccessHash: 2002}, Title: "bucket-b"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("channels = %#v, want %#v", got, want)
	}
	if len(main.calls) != 3 || len(archive.calls) != 2 {
		t.Fatalf("query calls = main:%d archive:%d, want 3 and 2", len(main.calls), len(archive.calls))
	}
	for _, request := range append(append([]dialogs.Request{}, main.calls...), archive.calls...) {
		if request.Limit != ownedDialogsBatchSize {
			t.Fatalf("request limit = %d, want %d", request.Limit, ownedDialogsBatchSize)
		}
	}
}

func TestCollectOwnedChannelsReturnsNoPartialResultsOnFailure(t *testing.T) {
	wantErr := errors.New("dialog lookup failed")
	query := &scriptedDialogsQuery{
		responses: []tg.MessagesDialogsClass{dialogsPage(dialogChannel{id: 1001, title: "a", creator: true, broadcast: true, topMessage: 1})},
		errAt:     2,
		err:       wantErr,
	}
	got, err := collectOwnedChannels(t.Context(), query)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if got != nil {
		t.Fatalf("partial channels returned after failure: %#v", got)
	}
}

func dialogsInvoker(foldersSeen *[]int, byFolder map[int]tg.MessagesDialogsClass) handlerInvoker {
	return func(_ context.Context, input bin.Encoder, output bin.Decoder) error {
		request, ok := input.(*tg.MessagesGetDialogsRequest)
		if !ok {
			return errors.New("resolution attempted a channel RPC without its access hash")
		}
		if !request.Flags.Has(1) {
			return errors.New("folder flag missing")
		}
		*foldersSeen = append(*foldersSeen, request.FolderID)
		if page, ok := byFolder[request.FolderID]; ok && request.OffsetID == 0 {
			return reply(output, page)
		}
		return reply(output, &tg.MessagesDialogs{})
	}
}

func TestResolveChannelUsesDialogHashInPrimaryAndArchivedFolders(t *testing.T) {
	for _, test := range []struct {
		name   string
		folder int
	}{{"primary broadcast", 0}, {"archived broadcast", 1}} {
		t.Run(test.name, func(t *testing.T) {
			var folders []int
			channel := &tg.Channel{ID: 81, AccessHash: 991, Broadcast: true, Photo: &tg.ChatPhotoEmpty{}}
			page := &tg.MessagesDialogs{Dialogs: []tg.DialogClass{&tg.Dialog{Peer: &tg.PeerChannel{ChannelID: 81}}}, Chats: []tg.ChatClass{channel}}
			client, _ := newTestClient(t, dialogsInvoker(&folders, map[int]tg.MessagesDialogsClass{test.folder: page}))

			peer, err := client.ResolveChannel(t.Context(), 81)
			if err != nil || peer != (tgclient.InputPeer{ChannelID: 81, AccessHash: 991}) {
				t.Fatalf("resolve = %+v, %v; want id 81 hash 991", peer, err)
			}
			if len(folders) != test.folder+1 {
				t.Fatalf("folders = %v, want stop at requested folder", folders)
			}
		})
	}
}

func TestResolveChannelContinuesAcrossDialogPages(t *testing.T) {
	pages := 0
	client, _ := newTestClient(t, func(_ context.Context, input bin.Encoder, output bin.Decoder) error {
		request := input.(*tg.MessagesGetDialogsRequest)
		pages++
		if pages == 1 {
			return reply(output, &tg.MessagesDialogsSlice{Count: 2,
				Dialogs:  []tg.DialogClass{&tg.Dialog{Peer: &tg.PeerChannel{ChannelID: 80}, TopMessage: 44}},
				Chats:    []tg.ChatClass{&tg.Channel{ID: 80, AccessHash: 990, Photo: &tg.ChatPhotoEmpty{}}},
				Messages: []tg.MessageClass{&tg.Message{ID: 44, Date: 100, PeerID: &tg.PeerChannel{ChannelID: 80}}},
			})
		}
		if request.OffsetID != 44 || request.OffsetDate != 100 {
			return errors.New("dialog pagination cursor lost")
		}
		return reply(output, &tg.MessagesDialogs{
			Dialogs: []tg.DialogClass{&tg.Dialog{Peer: &tg.PeerChannel{ChannelID: 81}}},
			Chats:   []tg.ChatClass{&tg.Channel{ID: 81, AccessHash: 991, Photo: &tg.ChatPhotoEmpty{}}},
		})
	})
	peer, err := client.ResolveChannel(t.Context(), 81)
	if err != nil || peer.AccessHash != 991 || pages != 2 {
		t.Fatalf("peer %+v, err %v, pages %d; want second-page hash 991", peer, err, pages)
	}
}

func TestResolveChannelRejectsInaccessibleEntities(t *testing.T) {
	for _, test := range []struct {
		name   string
		entity tg.ChatClass
	}{
		{"left", &tg.Channel{ID: 81, AccessHash: 991, Left: true, Photo: &tg.ChatPhotoEmpty{}}},
		{"minimal", &tg.Channel{ID: 81, AccessHash: 991, Min: true, Photo: &tg.ChatPhotoEmpty{}}},
		{"forbidden", &tg.ChannelForbidden{ID: 81, AccessHash: 991}},
		{"missing hash", &tg.Channel{ID: 81, Photo: &tg.ChatPhotoEmpty{}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, _ := newTestClient(t, func(_ context.Context, _ bin.Encoder, output bin.Decoder) error {
				return reply(output, &tg.MessagesDialogs{
					Dialogs: []tg.DialogClass{&tg.Dialog{Peer: &tg.PeerChannel{ChannelID: 81}}}, Chats: []tg.ChatClass{test.entity},
				})
			})
			peer, err := client.ResolveChannel(t.Context(), 81)
			if !errors.Is(err, tgclient.ErrChannelUnavailable) || peer != (tgclient.InputPeer{}) {
				t.Fatalf("resolve = %+v, %v; want ErrChannelUnavailable", peer, err)
			}
		})
	}
}

func TestResolveChannelPreservesRPCFailureAndCancellation(t *testing.T) {
	failure := errors.New("dialog access failed")
	calls := 0
	client, _ := newTestClient(t, func(context.Context, bin.Encoder, bin.Decoder) error { calls++; return failure })
	if _, err := client.ResolveChannel(t.Context(), 81); !errors.Is(err, failure) {
		t.Fatalf("error %v, want original RPC failure", err)
	}
	if calls != 1 {
		t.Fatalf("RPC calls %d, want stop on first failure", calls)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := client.ResolveChannel(ctx, 81); !errors.Is(err, context.Canceled) {
		t.Fatalf("error %v, want context canceled", err)
	}
	if calls != 1 {
		t.Fatal("canceled resolution reached Telegram")
	}
}

func TestResolveChannelRefusesInvalidIDBeforeAnyCall(t *testing.T) {
	client, _ := newTestClient(t, noCalls(t))
	if _, err := client.ResolveChannel(t.Context(), 0); !errors.Is(err, tgclient.ErrChannelUnavailable) {
		t.Fatalf("err = %v, want ErrChannelUnavailable", err)
	}
}

func TestCreateChannelBroadcastThroughChannelGate(t *testing.T) {
	var request *tg.ChannelsCreateChannelRequest
	client, gate := newTestClient(t, func(_ context.Context, input bin.Encoder, output bin.Decoder) error {
		request = input.(*tg.ChannelsCreateChannelRequest)
		return reply(output, &tg.Updates{Chats: []tg.ChatClass{&tg.Channel{ID: 555, AccessHash: 777, Photo: &tg.ChatPhotoEmpty{}}}})
	})
	peer, err := client.CreateChannel(t.Context(), "bucket", "about")
	if err != nil || peer != (tgclient.InputPeer{ChannelID: 555, AccessHash: 777}) {
		t.Fatalf("create = %+v, %v", peer, err)
	}
	if !request.Broadcast || request.Megagroup || request.Title != "bucket" || request.About != "about" {
		t.Fatalf("request = %+v", request)
	}
	if !reflect.DeepEqual(gate.recorded(), []tgclient.WriteClass{tgclient.WriteChannel}) {
		t.Fatalf("gate classes = %v", gate.recorded())
	}
}

func TestCreateChannelWithoutChannelInAnswerIsUnknownOutcome(t *testing.T) {
	client, _ := newTestClient(t, func(_ context.Context, _ bin.Encoder, output bin.Decoder) error {
		return reply(output, &tg.Updates{})
	})
	if _, err := client.CreateChannel(t.Context(), "b", ""); !errors.Is(err, tgclient.ErrSendOutcomeUnknown) {
		t.Fatalf("err = %v, want ErrSendOutcomeUnknown", err)
	}
}

func TestSelfReturnsAccount(t *testing.T) {
	client, _ := newTestClient(t, func(_ context.Context, _ bin.Encoder, output bin.Decoder) error {
		return reply(output, &tg.UserClassVector{Elems: []tg.UserClass{&tg.User{ID: 42, Self: true, FirstName: "Ada", Username: "ada"}}})
	})
	account, err := client.Self(t.Context())
	if err != nil || account != (tgclient.Account{UserID: 42, FirstName: "Ada", Username: "ada"}) {
		t.Fatalf("self = %+v, %v", account, err)
	}
}

func TestSelfUnregisteredKeyIsNotAuthorized(t *testing.T) {
	client, _ := newTestClient(t, func(context.Context, bin.Encoder, bin.Decoder) error { return rpcError(401, "AUTH_KEY_UNREGISTERED") })
	if _, err := client.Self(t.Context()); !errors.Is(err, tgclient.ErrNotAuthorized) {
		t.Fatalf("err = %v, want ErrNotAuthorized", err)
	}
}
