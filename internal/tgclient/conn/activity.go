package conn

import (
	"context"
	"sync/atomic"

	"github.com/gotd/td/tg"
)

const defaultSignalBuffer = 256

type Activity struct {
	ch      chan int64
	dropped atomic.Int64
}

func NewActivity(buffer int) *Activity {
	if buffer <= 0 {
		buffer = defaultSignalBuffer
	}
	return &Activity{ch: make(chan int64, buffer)}
}

func (a *Activity) Signals() <-chan int64 {
	return a.ch
}

func (a *Activity) Dropped() int64 {
	return a.dropped.Load()
}

func (a *Activity) Signal(channelID int64) {
	if channelID <= 0 {
		return
	}
	select {
	case a.ch <- channelID:
	default:
		a.dropped.Add(1)
	}
}

func (a *Activity) Handle(_ context.Context, updates tg.UpdatesClass) error {
	for _, channelID := range ChannelIDsFromUpdates(updates) {
		a.Signal(channelID)
	}
	return nil
}

func ChannelIDsFromUpdates(updates tg.UpdatesClass) []int64 {
	seen := make(map[int64]struct{})
	var out []int64
	add := func(channelID int64) {
		if channelID <= 0 {
			return
		}
		if _, ok := seen[channelID]; ok {
			return
		}
		seen[channelID] = struct{}{}
		out = append(out, channelID)
	}

	switch u := updates.(type) {
	case *tg.UpdateShort:
		add(channelIDFromUpdate(u.Update))
	case *tg.Updates:
		for _, item := range u.Updates {
			add(channelIDFromUpdate(item))
		}
	case *tg.UpdatesCombined:
		for _, item := range u.Updates {
			add(channelIDFromUpdate(item))
		}
	}
	return out
}

func channelIDFromUpdate(update tg.UpdateClass) int64 {
	switch u := update.(type) {
	case *tg.UpdateNewChannelMessage:
		return channelIDFromMessage(u.Message)
	case *tg.UpdateEditChannelMessage:
		return channelIDFromMessage(u.Message)
	case *tg.UpdateDeleteChannelMessages:
		return u.ChannelID
	default:
		return 0
	}
}

func channelIDFromMessage(message tg.MessageClass) int64 {
	switch msg := message.(type) {
	case *tg.Message:
		return channelIDFromPeer(msg.GetPeerID())
	case *tg.MessageService:
		return channelIDFromPeer(msg.GetPeerID())
	default:
		return 0
	}
}

func channelIDFromPeer(peer tg.PeerClass) int64 {
	if p, ok := peer.(*tg.PeerChannel); ok {
		return p.ChannelID
	}
	return 0
}
