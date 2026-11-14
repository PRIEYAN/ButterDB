package rpc

import (
	"context"
	"sync"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

type channelEvent struct {
	pts     int
	message *tg.Message
	deleted int
}

type fakeChannel struct {
	mu          sync.Mutex
	channelID   int64
	accessHash  int64
	nextID      int
	pts         int
	messages    map[int]*tg.Message
	events      []channelEvent
	tooLong     bool
	floodWaits  int
	diffCalls   int
	fullCalls   int
	deleteSizes []int
}

func newFakeChannel(channelID, accessHash int64) *fakeChannel {
	return &fakeChannel{channelID: channelID, accessHash: accessHash, nextID: 1, pts: 1, messages: map[int]*tg.Message{}}
}

func (f *fakeChannel) post(text string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.nextID
	f.nextID++
	f.pts++
	message := &tg.Message{ID: id, Date: 1000 + id, Message: text, PeerID: &tg.PeerChannel{ChannelID: f.channelID}}
	f.messages[id] = message
	f.events = append(f.events, channelEvent{pts: f.pts, message: message})
	return id
}

func (f *fakeChannel) remove(ids ...int) {
	for _, id := range ids {
		delete(f.messages, id)
		f.pts++
		f.events = append(f.events, channelEvent{pts: f.pts, deleted: id})
	}
}

func (f *fakeChannel) invoke(_ context.Context, input bin.Encoder, output bin.Decoder) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.floodWaits > 0 {
		f.floodWaits--
		return rpcError(420, "FLOOD_WAIT_9")
	}
	switch request := input.(type) {
	case *tg.MessagesGetHistoryRequest:
		return reply(output, f.history(request))
	case *tg.ChannelsGetMessagesRequest:
		return reply(output, f.lookup(request))
	case *tg.ChannelsDeleteMessagesRequest:
		f.deleteSizes = append(f.deleteSizes, len(request.ID))
		f.remove(request.ID...)
		return reply(output, &tg.MessagesAffectedMessages{})
	case *tg.UpdatesGetChannelDifferenceRequest:
		f.diffCalls++
		return reply(output, f.difference(request))
	case *tg.ChannelsGetFullChannelRequest:
		f.fullCalls++
		return reply(output, &tg.MessagesChatFull{FullChat: &tg.ChannelFull{ID: f.channelID, Pts: f.pts, ChatPhoto: &tg.PhotoEmpty{}}})
	}
	return rpcError(400, "METHOD_UNKNOWN")
}

func (f *fakeChannel) history(request *tg.MessagesGetHistoryRequest) *tg.MessagesChannelMessages {
	var page []tg.MessageClass
	for id := f.nextID - 1; id >= 1 && len(page) < request.Limit; id-- {
		if request.OffsetID != 0 && id >= request.OffsetID {
			continue
		}
		if message, ok := f.messages[id]; ok {
			page = append(page, message)
		}
	}
	return &tg.MessagesChannelMessages{Messages: page}
}

func (f *fakeChannel) lookup(request *tg.ChannelsGetMessagesRequest) *tg.MessagesChannelMessages {
	out := &tg.MessagesChannelMessages{}
	for _, input := range request.ID {
		id := input.(*tg.InputMessageID).ID
		if message, ok := f.messages[id]; ok {
			out.Messages = append(out.Messages, message)
			continue
		}
		out.Messages = append(out.Messages, &tg.MessageEmpty{ID: id})
	}
	return out
}

func (f *fakeChannel) difference(request *tg.UpdatesGetChannelDifferenceRequest) tg.UpdatesChannelDifferenceClass {
	if f.tooLong {
		f.tooLong = false
		return &tg.UpdatesChannelDifferenceTooLong{Dialog: &tg.Dialog{Pts: f.pts, Peer: &tg.PeerChannel{ChannelID: f.channelID}}}
	}
	var pending []channelEvent
	for _, event := range f.events {
		if event.pts > request.Pts {
			pending = append(pending, event)
		}
	}
	if len(pending) == 0 {
		return &tg.UpdatesChannelDifferenceEmpty{Final: true, Pts: f.pts}
	}
	final := true
	if len(pending) > request.Limit {
		pending = pending[:request.Limit]
		final = false
	}
	out := &tg.UpdatesChannelDifference{Final: final, Pts: pending[len(pending)-1].pts}
	var deleted []int
	for _, event := range pending {
		if event.message != nil {
			out.NewMessages = append(out.NewMessages, event.message)
			continue
		}
		deleted = append(deleted, event.deleted)
	}
	if len(deleted) > 0 {
		out.OtherUpdates = append(out.OtherUpdates, &tg.UpdateDeleteChannelMessages{Messages: deleted})
	}
	return out
}
