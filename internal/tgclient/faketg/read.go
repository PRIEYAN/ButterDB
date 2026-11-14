package faketg

import (
	"context"
	"fmt"
	"io"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

func (c *Client) ResolveDocument(ctx context.Context, peer tgclient.InputPeer, msgID int64) (tgclient.DocumentRef, error) {
	if err := checkPeer(peer); err != nil {
		return tgclient.DocumentRef{}, err
	}
	if _, err := c.enter(ctx, MethodResolveDocument, peer.ChannelID); err != nil {
		return tgclient.DocumentRef{}, err
	}
	c.n.mu.Lock()
	defer c.n.mu.Unlock()
	return c.resolveLocked(peer, msgID)
}

func (c *Client) ResolveDocuments(ctx context.Context, peer tgclient.InputPeer, msgIDs []int64) ([]tgclient.DocumentRef, error) {
	if err := checkPeer(peer); err != nil {
		return nil, err
	}
	if _, err := c.enter(ctx, MethodResolveDocuments, peer.ChannelID); err != nil {
		return nil, err
	}
	c.n.mu.Lock()
	defer c.n.mu.Unlock()
	refs := make([]tgclient.DocumentRef, 0, len(msgIDs))
	for _, msgID := range msgIDs {
		ref, err := c.resolveLocked(peer, msgID)
		if err != nil {
			return nil, fmt.Errorf("message %d: %w", msgID, err)
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

func (c *Client) resolveLocked(peer tgclient.InputPeer, msgID int64) (tgclient.DocumentRef, error) {
	ch, err := c.peerChannelLocked(peer, false)
	if err != nil {
		return tgclient.DocumentRef{}, err
	}
	msg, ok := ch.message(msgID)
	if !ok || msg.Placeholder {
		return tgclient.DocumentRef{}, fmt.Errorf("faketg: message %d: %w", msgID, tgclient.ErrMessageNotFound)
	}
	if _, isDoc := ch.docs[msgID]; !isDoc || msg.MediaSize <= 0 {
		return tgclient.DocumentRef{}, fmt.Errorf("faketg: message %d is not a document: %w", msgID, tgclient.ErrMessageNotFound)
	}
	return c.documentRefLocked(ch, msgID), nil
}

func checkRangePreconditions(offset int64, length int) error {
	if offset < 0 {
		return fmt.Errorf("faketg: %w: negative offset", ErrRangePrecondition)
	}
	if offset%rangeAlignment != 0 {
		return fmt.Errorf("faketg: %w: offset %d is not %d-aligned", ErrRangePrecondition, offset, rangeAlignment)
	}
	if length > rangeMaxBytes {
		return fmt.Errorf("faketg: %w: length %d exceeds %d", ErrRangePrecondition, length, rangeMaxBytes)
	}
	if offset/rangeMaxBytes != (offset+int64(length)-1)/rangeMaxBytes {
		return fmt.Errorf("faketg: %w: range crosses a %d-byte boundary", ErrRangePrecondition, rangeMaxBytes)
	}
	return nil
}

func (c *Client) ReadDocumentRange(ctx context.Context, ref tgclient.DocumentRef, offset int64, dst []byte) (int, error) {
	if len(dst) == 0 {
		return 0, nil
	}
	if err := checkRangePreconditions(offset, len(dst)); err != nil {
		return 0, err
	}
	fault, err := c.enter(ctx, MethodReadDocumentRange, ref.Peer.ChannelID)
	if err != nil {
		return 0, err
	}
	c.n.mu.Lock()
	defer c.n.mu.Unlock()
	c.readCalls++
	return c.readLocked(ref, offset, dst, fault)
}

func (c *Client) readLocked(ref tgclient.DocumentRef, offset int64, dst []byte, fault Fault) (int, error) {
	if err := c.requireAuthLocked(); err != nil {
		return 0, err
	}
	ch, err := c.n.channelLocked(ref.Peer.ChannelID)
	if err != nil {
		return 0, err
	}
	if _, member := ch.members[c.userID]; !member {
		return 0, fmt.Errorf("faketg: channel %d: %w", ch.id, tgclient.ErrChannelUnavailable)
	}
	doc, ok := ch.docs[ref.MsgID]
	if !ok || !doc.hasBody || (ref.DocumentID != 0 && ref.DocumentID != doc.id) {
		return 0, fmt.Errorf("faketg: message %d: %w", ref.MsgID, tgclient.ErrMessageNotFound)
	}
	if fault.Kind == FaultExpireFileReference {
		doc.refGen++
		return 0, fmt.Errorf("faketg: message %d: %w", ref.MsgID, tgclient.ErrFileReferenceExpired)
	}
	if gen, parsed := decodeFileReference(ref.FileReference); len(ref.FileReference) > 0 && (!parsed || gen != doc.refGen) {
		return 0, fmt.Errorf("faketg: message %d: %w", ref.MsgID, tgclient.ErrFileReferenceExpired)
	}
	if fault.Kind == FaultCDNRedirect {
		c.cdnRedirects++
		if fault.Err != nil {
			return 0, fault.Err
		}
	}
	if offset+int64(len(dst)) > int64(len(doc.data)) {
		return 0, io.ErrUnexpectedEOF
	}
	return copy(dst, doc.data[offset:offset+int64(len(dst))]), nil
}
