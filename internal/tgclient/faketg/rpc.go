package faketg

import (
	"context"
	"fmt"
	"sort"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

func (c *Client) CreateChannel(ctx context.Context, title, about string) (tgclient.InputPeer, error) {
	fault, err := c.enter(ctx, MethodCreateChannel, 0)
	if err != nil {
		return tgclient.InputPeer{}, err
	}
	c.n.mu.Lock()
	defer c.n.mu.Unlock()
	if err := c.requireAuthLocked(); err != nil {
		return tgclient.InputPeer{}, err
	}
	if fault.Kind == FaultOutcomeUnknown {
		if fault.Delivered {
			c.createChannelLocked(title, about)
		}
		return tgclient.InputPeer{}, fault.sendFailure()
	}
	return c.createChannelLocked(title, about), nil
}

func (c *Client) createChannelLocked(title, about string) tgclient.InputPeer {
	id := c.n.nextChannelID
	c.n.nextChannelID++
	c.n.newChannelLocked(id, title, about, c)
	return peerFor(id, c.userID)
}

func (c *Client) GetHistory(ctx context.Context, peer tgclient.InputPeer, offsetID int64, limit int) ([]tgclient.HistoryMessage, error) {
	if err := checkPeer(peer); err != nil {
		return nil, err
	}
	if _, err := c.enter(ctx, MethodGetHistory, peer.ChannelID); err != nil {
		return nil, err
	}
	c.n.mu.Lock()
	defer c.n.mu.Unlock()
	ch, err := c.peerChannelLocked(peer, false)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > historyPageMax {
		limit = historyPageMax
	}
	out := make([]tgclient.HistoryMessage, 0, limit)
	for i := len(ch.msgs) - 1; i >= 0 && len(out) < limit; i-- {
		if offsetID > 0 && ch.msgs[i].MsgID >= offsetID {
			continue
		}
		out = append(out, ch.msgs[i])
	}
	return out, nil
}

func (c *Client) GetChannelPts(ctx context.Context, peer tgclient.InputPeer) (int, error) {
	if err := checkPeer(peer); err != nil {
		return 0, err
	}
	if _, err := c.enter(ctx, MethodGetChannelPts, peer.ChannelID); err != nil {
		return 0, err
	}
	c.n.mu.Lock()
	defer c.n.mu.Unlock()
	ch, err := c.peerChannelLocked(peer, false)
	if err != nil {
		return 0, err
	}
	return ch.pts, nil
}

func (c *Client) GetChannelDifference(ctx context.Context, peer tgclient.InputPeer, pts int, limit int) (tgclient.ChannelDifference, error) {
	if err := checkPeer(peer); err != nil {
		return tgclient.ChannelDifference{}, err
	}
	if pts < 0 {
		return tgclient.ChannelDifference{}, fmt.Errorf("faketg: %w: negative pts", ErrInvalidArgument)
	}
	fault, err := c.enter(ctx, MethodGetChannelDifference, peer.ChannelID)
	if err != nil {
		return tgclient.ChannelDifference{}, err
	}
	c.n.mu.Lock()
	defer c.n.mu.Unlock()
	ch, err := c.peerChannelLocked(peer, false)
	if err != nil {
		return tgclient.ChannelDifference{}, err
	}
	if fault.Kind == FaultDifferenceTooLong {
		return tgclient.ChannelDifference{TooLong: true, Final: true, Pts: ch.pts}, nil
	}
	if limit <= 0 || limit > historyPageMax {
		limit = historyPageMax
	}
	return ch.differenceLocked(pts, limit), nil
}

func (ch *channelState) differenceLocked(pts, limit int) tgclient.ChannelDifference {
	var pending []channelEvent
	for _, ev := range ch.events {
		if ev.pts > pts {
			pending = append(pending, ev)
		}
	}
	if len(pending) == 0 {
		return tgclient.ChannelDifference{Pts: ch.pts, Final: true}
	}
	page := pending
	if len(page) > limit {
		page = page[:limit]
	}
	out := tgclient.ChannelDifference{Pts: page[len(page)-1].pts, Final: len(page) == len(pending)}
	changed := make(map[int64]struct{})
	for _, ev := range page {
		if ev.kind == eventDelete {
			out.DeletedMessageIDs = append(out.DeletedMessageIDs, ev.deletedID)
			continue
		}
		changed[ev.msgID] = struct{}{}
	}
	ids := make([]int64, 0, len(changed))
	for id := range changed {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		if msg, ok := ch.message(id); ok {
			out.Messages = append(out.Messages, msg)
		}
	}
	return out
}

func (c *Client) GetMessages(ctx context.Context, peer tgclient.InputPeer, msgIDs []int64) ([]tgclient.HistoryMessage, error) {
	if err := checkPeer(peer); err != nil {
		return nil, err
	}
	if _, err := c.enter(ctx, MethodGetMessages, peer.ChannelID); err != nil {
		return nil, err
	}
	c.n.mu.Lock()
	defer c.n.mu.Unlock()
	ch, err := c.peerChannelLocked(peer, false)
	if err != nil {
		return nil, err
	}
	out := make([]tgclient.HistoryMessage, 0, len(msgIDs))
	seen := make(map[int64]struct{}, len(msgIDs))
	for _, id := range msgIDs {
		if _, dup := seen[id]; dup || id <= 0 {
			continue
		}
		seen[id] = struct{}{}
		if msg, ok := ch.message(id); ok {
			out = append(out, msg)
		}
	}
	return out, nil
}

func (c *Client) DeleteMessages(ctx context.Context, peer tgclient.InputPeer, msgIDs []int64) error {
	if err := checkPeer(peer); err != nil {
		return err
	}
	fault, err := c.enter(ctx, MethodDeleteMessages, peer.ChannelID)
	if err != nil {
		return err
	}
	c.n.mu.Lock()
	defer c.n.mu.Unlock()
	ch, err := c.peerChannelLocked(peer, true)
	if err != nil {
		return err
	}
	ids := msgIDs
	if fault.Kind == FaultPartialDelete && fault.Count < len(ids) {
		ids = ids[:fault.Count]
	}
	for start := 0; start < len(ids); start += deleteBatchSize {
		batch := ids[start:min(start+deleteBatchSize, len(ids))]
		ch.deletedBatches = append(ch.deletedBatches, append([]int64(nil), batch...))
		ch.deleteLocked(batch)
	}
	if fault.Kind == FaultPartialDelete {
		if fault.Err != nil {
			return fault.Err
		}
		return ErrInjectedTransport
	}
	return nil
}

func (c *Client) ListOwnedChannels(ctx context.Context) ([]tgclient.Channel, error) {
	if _, err := c.enter(ctx, MethodListOwnedChannels, 0); err != nil {
		return nil, err
	}
	c.n.mu.Lock()
	defer c.n.mu.Unlock()
	if err := c.requireAuthLocked(); err != nil {
		return nil, err
	}
	var out []tgclient.Channel
	for _, ch := range c.n.channels {
		if ch.ownerID == c.userID {
			out = append(out, tgclient.Channel{Peer: peerFor(ch.id, c.userID), Title: ch.title})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Peer.ChannelID < out[j].Peer.ChannelID })
	return out, nil
}

func (c *Client) ResolveChannel(ctx context.Context, channelID int64) (tgclient.InputPeer, error) {
	if channelID <= 0 {
		return tgclient.InputPeer{}, fmt.Errorf("faketg: %w: channel id", tgclient.ErrChannelUnavailable)
	}
	if _, err := c.enter(ctx, MethodResolveChannel, channelID); err != nil {
		return tgclient.InputPeer{}, err
	}
	c.n.mu.Lock()
	defer c.n.mu.Unlock()
	if err := c.requireAuthLocked(); err != nil {
		return tgclient.InputPeer{}, err
	}
	ch, err := c.n.channelLocked(channelID)
	if err != nil {
		return tgclient.InputPeer{}, err
	}
	if _, member := ch.members[c.userID]; !member {
		return tgclient.InputPeer{}, fmt.Errorf("faketg: channel %d: %w", channelID, tgclient.ErrChannelUnavailable)
	}
	return peerFor(channelID, c.userID), nil
}

func (c *Client) SendToSavedMessages(ctx context.Context, text string) (int64, error) {
	if text == "" {
		return 0, fmt.Errorf("faketg: %w: saved message text is empty", ErrInvalidArgument)
	}
	fault, err := c.enter(ctx, MethodSendToSavedMessages, 0)
	if err != nil {
		return 0, err
	}
	msgID, err := c.deliverSaved(text, fault)
	if err != nil {
		return msgID, err
	}
	return msgID, fault.sendFailure()
}

func (c *Client) deliverSaved(text string, fault Fault) (int64, error) {
	c.n.mu.Lock()
	defer c.n.mu.Unlock()
	if err := c.requireAuthLocked(); err != nil {
		return 0, err
	}
	if fault.Kind == FaultOutcomeUnknown && !fault.Delivered {
		return 0, nil
	}
	id := c.nextSavedID
	c.nextSavedID++
	c.saved = append(c.saved, SentSaved{MsgID: id, Text: text, Date: c.n.nowUnix()})
	return reportedMsgID(fault, id), nil
}

func (c *Client) Self(ctx context.Context) (tgclient.Account, error) {
	if _, err := c.enter(ctx, MethodSelf, 0); err != nil {
		return tgclient.Account{}, err
	}
	c.n.mu.Lock()
	defer c.n.mu.Unlock()
	if err := c.requireAuthLocked(); err != nil {
		return tgclient.Account{}, err
	}
	return c.profile, nil
}
