package faketg

import (
	"context"
	"fmt"
	"io"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

func (c *Client) SendControlIdempotent(ctx context.Context, req tgclient.SendControlRequest) (int64, error) {
	if err := checkPeer(req.Peer); err != nil {
		return 0, err
	}
	if req.RandomID == 0 {
		return 0, fmt.Errorf("faketg: %w: random id must be non-zero", ErrInvalidArgument)
	}
	fault, err := c.enter(ctx, MethodSendControlIdempotent, req.Peer.ChannelID)
	if err != nil {
		return 0, err
	}
	msgID, err := c.deliverControl(req, fault)
	if err != nil {
		return msgID, err
	}
	return msgID, fault.sendFailure()
}

func (c *Client) deliverControl(req tgclient.SendControlRequest, fault Fault) (int64, error) {
	c.n.mu.Lock()
	defer c.n.mu.Unlock()
	ch, err := c.peerChannelLocked(req.Peer, true)
	if err != nil {
		return 0, err
	}
	key := dedupeKey{userID: c.userID, randomID: req.RandomID}
	if msgID, ok := ch.dedupe[key]; ok {
		return reportedMsgID(fault, msgID), nil
	}
	if fault.Kind == FaultOutcomeUnknown && !fault.Delivered {
		return 0, nil
	}
	msg, err := c.n.insertLocked(ch, tgclient.HistoryMessage{Text: req.Text}, nil, "")
	if err != nil {
		return 0, err
	}
	ch.dedupe[key] = msg.MsgID
	ch.sentControls = append(ch.sentControls, SentControl{
		AccountID: c.userID, Peer: req.Peer, Text: req.Text, MsgID: msg.MsgID, RandomID: req.RandomID,
	})
	return reportedMsgID(fault, msg.MsgID), nil
}

func reportedMsgID(fault Fault, msgID int64) int64 {
	if fault.Kind == FaultOutcomeUnknown {
		return 0
	}
	return msgID
}

func (c *Client) SendFileIdempotent(ctx context.Context, req tgclient.SendFileRequest) (tgclient.SendFileResult, error) {
	if err := checkPeer(req.Peer); err != nil {
		return tgclient.SendFileResult{}, err
	}
	if req.RandomID == 0 {
		return tgclient.SendFileResult{}, fmt.Errorf("faketg: %w: random id must be non-zero", ErrInvalidArgument)
	}
	if req.Body == nil || req.Size < 0 {
		return tgclient.SendFileResult{}, fmt.Errorf("faketg: %w: body and size", ErrInvalidArgument)
	}
	fault, err := c.enter(ctx, MethodSendFileIdempotent, req.Peer.ChannelID)
	if err != nil {
		return tgclient.SendFileResult{}, err
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return tgclient.SendFileResult{}, fmt.Errorf("faketg: read body: %w", err)
	}
	if req.Size > 0 && int64(len(body)) != req.Size {
		return tgclient.SendFileResult{}, fmt.Errorf("faketg: %w: body is %d bytes, request declared %d", ErrInvalidArgument, len(body), req.Size)
	}
	if err := ctx.Err(); err != nil {
		return tgclient.SendFileResult{}, err
	}
	result, err := c.deliverFile(req, body, fault)
	if err != nil {
		return result, err
	}
	return result, fault.sendFailure()
}

func (c *Client) deliverFile(req tgclient.SendFileRequest, body []byte, fault Fault) (tgclient.SendFileResult, error) {
	c.n.mu.Lock()
	defer c.n.mu.Unlock()
	ch, err := c.peerChannelLocked(req.Peer, true)
	if err != nil {
		return tgclient.SendFileResult{}, err
	}
	key := dedupeKey{userID: c.userID, randomID: req.RandomID}
	if msgID, ok := ch.dedupe[key]; ok {
		return c.fileResultLocked(ch, msgID, fault), nil
	}
	if fault.Kind == FaultOutcomeUnknown && !fault.Delivered {
		return tgclient.SendFileResult{}, nil
	}
	msg, err := c.n.insertLocked(ch, tgclient.HistoryMessage{Text: req.Caption, DocumentName: req.FileName}, nonNilBody(body), req.FileName)
	if err != nil {
		return tgclient.SendFileResult{}, err
	}
	ch.dedupe[key] = msg.MsgID
	ch.sentFiles = append(ch.sentFiles, SentFile{
		AccountID: c.userID, Peer: req.Peer, Name: req.FileName, Caption: req.Caption,
		Size: int64(len(body)), MsgID: msg.MsgID, RandomID: req.RandomID,
	})
	return c.fileResultLocked(ch, msg.MsgID, fault), nil
}

func (c *Client) fileResultLocked(ch *channelState, msgID int64, fault Fault) tgclient.SendFileResult {
	if fault.Kind == FaultOutcomeUnknown {
		return tgclient.SendFileResult{}
	}
	return tgclient.SendFileResult{MsgID: msgID, Document: c.documentRefLocked(ch, msgID)}
}

func (c *Client) documentRefLocked(ch *channelState, msgID int64) tgclient.DocumentRef {
	ref := tgclient.DocumentRef{Peer: peerFor(ch.id, c.userID), MsgID: msgID}
	doc, ok := ch.docs[msgID]
	if !ok {
		return ref
	}
	msg, _ := ch.message(msgID)
	ref.Size = msg.MediaSize
	ref.DocumentID = doc.id
	ref.AccessHash = doc.accessHash
	ref.FileReference = encodeFileReference(doc.refGen)
	ref.DCID = c.n.documentDC
	return ref
}

func encodeFileReference(gen uint64) []byte {
	out := make([]byte, 8)
	for i := 0; i < 8; i++ {
		out[7-i] = byte(gen >> (8 * i))
	}
	return out
}

func decodeFileReference(ref []byte) (uint64, bool) {
	if len(ref) != 8 {
		return 0, false
	}
	var gen uint64
	for _, b := range ref {
		gen = gen<<8 | uint64(b)
	}
	return gen, true
}
