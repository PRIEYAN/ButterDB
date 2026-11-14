package faketg

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

type eventKind int

const (
	eventNew eventKind = iota + 1
	eventDelete
)

type channelEvent struct {
	pts       int
	kind      eventKind
	msgID     int64
	deletedID int64
}

type dedupeKey struct {
	userID   int64
	randomID int64
}

type document struct {
	id         int64
	accessHash int64
	refGen     uint64
	name       string
	data       []byte
	hasBody    bool
}

type SentControl struct {
	AccountID int64
	Peer      tgclient.InputPeer
	Text      string
	MsgID     int64
	RandomID  int64
}

type SentFile struct {
	AccountID int64
	Peer      tgclient.InputPeer
	Name      string
	Caption   string
	Size      int64
	MsgID     int64
	RandomID  int64
}

type channelState struct {
	id             int64
	title          string
	about          string
	ownerID        int64
	members        map[int64]bool
	msgs           []tgclient.HistoryMessage
	docs           map[int64]*document
	nextMsgID      int64
	pts            int
	events         []channelEvent
	dedupe         map[dedupeKey]int64
	sentControls   []SentControl
	sentFiles      []SentFile
	deletedBatches [][]int64
}

func newChannelState(id int64, title, about string, ownerID int64) *channelState {
	return &channelState{
		id:        id,
		title:     title,
		about:     about,
		ownerID:   ownerID,
		members:   make(map[int64]bool),
		docs:      make(map[int64]*document),
		nextMsgID: 1,
		pts:       1,
		dedupe:    make(map[dedupeKey]int64),
	}
}

func (ch *channelState) indexOf(msgID int64) int {
	i := sort.Search(len(ch.msgs), func(i int) bool { return ch.msgs[i].MsgID >= msgID })
	if i < len(ch.msgs) && ch.msgs[i].MsgID == msgID {
		return i
	}
	return -1
}

func (ch *channelState) message(msgID int64) (tgclient.HistoryMessage, bool) {
	i := ch.indexOf(msgID)
	if i < 0 {
		return tgclient.HistoryMessage{}, false
	}
	return ch.msgs[i], true
}

func (ch *channelState) recordEvent(kind eventKind, msgID, deletedID int64) {
	ch.pts++
	ch.events = append(ch.events, channelEvent{pts: ch.pts, kind: kind, msgID: msgID, deletedID: deletedID})
}

func (n *Network) insertLocked(ch *channelState, msg tgclient.HistoryMessage, body []byte, docName string) (tgclient.HistoryMessage, error) {
	if msg.MsgID == 0 {
		msg.MsgID = ch.nextMsgID
	}
	if msg.MsgID < 0 {
		return msg, fmt.Errorf("faketg: negative message id %d", msg.MsgID)
	}
	if ch.indexOf(msg.MsgID) >= 0 {
		return msg, fmt.Errorf("faketg: message %d already exists in channel %d", msg.MsgID, ch.id)
	}
	if msg.MsgID >= ch.nextMsgID {
		ch.nextMsgID = msg.MsgID + 1
	}
	if msg.Date == 0 {
		msg.Date = n.nowUnix()
	}
	if body != nil {
		msg.HasMedia = true
		msg.MediaSize = int64(len(body))
	}
	i := sort.Search(len(ch.msgs), func(i int) bool { return ch.msgs[i].MsgID > msg.MsgID })
	ch.msgs = append(ch.msgs, tgclient.HistoryMessage{})
	copy(ch.msgs[i+1:], ch.msgs[i:])
	ch.msgs[i] = msg
	if msg.HasMedia && !msg.Placeholder {
		n.nextDocumentID++
		id := n.nextDocumentID
		ch.docs[msg.MsgID] = &document{
			id:         id,
			accessHash: id*31 + 17,
			refGen:     1,
			name:       docName,
			data:       bytes.Clone(body),
			hasBody:    body != nil,
		}
	}
	ch.recordEvent(eventNew, msg.MsgID, 0)
	return msg, nil
}

func (ch *channelState) deleteLocked(ids []int64) []int64 {
	var removed []int64
	for _, id := range ids {
		i := ch.indexOf(id)
		if i < 0 {
			continue
		}
		ch.msgs = append(ch.msgs[:i], ch.msgs[i+1:]...)
		delete(ch.docs, id)
		ch.recordEvent(eventDelete, 0, id)
		removed = append(removed, id)
	}
	return removed
}

func (n *Network) SeedHistory(channelID int64, msgs ...tgclient.HistoryMessage) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	ch, err := n.channelLocked(channelID)
	if err != nil {
		return err
	}
	for _, m := range msgs {
		if _, err := n.insertLocked(ch, m, nil, m.DocumentName); err != nil {
			return err
		}
	}
	return nil
}

func (n *Network) SeedDocument(channelID int64, name, caption string, body []byte) (int64, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	ch, err := n.channelLocked(channelID)
	if err != nil {
		return 0, err
	}
	msg, err := n.insertLocked(ch, tgclient.HistoryMessage{Text: caption, DocumentName: name}, nonNilBody(body), name)
	if err != nil {
		return 0, err
	}
	return msg.MsgID, nil
}

func (n *Network) SeedDocumentBody(channelID, msgID int64, body []byte) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	ch, err := n.channelLocked(channelID)
	if err != nil {
		return err
	}
	doc, ok := ch.docs[msgID]
	if !ok {
		return fmt.Errorf("faketg: message %d in channel %d: %w", msgID, channelID, tgclient.ErrMessageNotFound)
	}
	doc.data = bytes.Clone(body)
	doc.hasBody = true
	return nil
}

func nonNilBody(body []byte) []byte {
	if body == nil {
		return []byte{}
	}
	return body
}

func (n *Network) DeleteOutOfBand(channelID int64, ids ...int64) ([]int64, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	ch, err := n.channelLocked(channelID)
	if err != nil {
		return nil, err
	}
	return ch.deleteLocked(ids), nil
}

func (n *Network) EditMessageText(channelID, msgID int64, text string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	ch, err := n.channelLocked(channelID)
	if err != nil {
		return err
	}
	i := ch.indexOf(msgID)
	if i < 0 {
		return fmt.Errorf("faketg: message %d in channel %d: %w", msgID, channelID, tgclient.ErrMessageNotFound)
	}
	ch.msgs[i].Text = text
	for j := range ch.sentControls {
		if ch.sentControls[j].MsgID == msgID {
			ch.sentControls[j].Text = text
		}
	}
	return nil
}

func (n *Network) ExpireFileReference(channelID, msgID int64) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	ch, err := n.channelLocked(channelID)
	if err != nil {
		return err
	}
	doc, ok := ch.docs[msgID]
	if !ok {
		return fmt.Errorf("faketg: message %d in channel %d: %w", msgID, channelID, tgclient.ErrMessageNotFound)
	}
	doc.refGen++
	return nil
}

func (n *Network) Messages(channelID int64) ([]tgclient.HistoryMessage, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	ch, err := n.channelLocked(channelID)
	if err != nil {
		return nil, err
	}
	return append([]tgclient.HistoryMessage(nil), ch.msgs...), nil
}

func (n *Network) Pts(channelID int64) (int, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	ch, err := n.channelLocked(channelID)
	if err != nil {
		return 0, err
	}
	return ch.pts, nil
}

func (n *Network) SentControls(channelID int64) []SentControl {
	n.mu.Lock()
	defer n.mu.Unlock()
	ch, ok := n.channels[channelID]
	if !ok {
		return nil
	}
	return append([]SentControl(nil), ch.sentControls...)
}

func (n *Network) SentFiles(channelID int64) []SentFile {
	n.mu.Lock()
	defer n.mu.Unlock()
	ch, ok := n.channels[channelID]
	if !ok {
		return nil
	}
	return append([]SentFile(nil), ch.sentFiles...)
}

func (n *Network) DeletedBatches(channelID int64) [][]int64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	ch, ok := n.channels[channelID]
	if !ok {
		return nil
	}
	out := make([][]int64, len(ch.deletedBatches))
	for i, b := range ch.deletedBatches {
		out[i] = append([]int64(nil), b...)
	}
	return out
}
