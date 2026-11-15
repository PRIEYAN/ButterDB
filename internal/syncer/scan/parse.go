package scan

import (
	"cmp"
	"slices"

	"github.com/PRIEYAN/ButterDB/internal/projection"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

type ParsedMessage struct {
	MsgID    int64
	Observed projection.Observed
}

func ParseHistoryPage(channelID int64, msgs []tgclient.HistoryMessage) []ParsedMessage {
	out := make([]ParsedMessage, 0, len(msgs))
	for _, m := range msgs {
		// Only headerless messages are skipped: unknown and malformed ops reach the projector as rejects (E5).
		if !wire.HasPrefix(m.Text) {
			continue
		}
		out = append(out, ParsedMessage{MsgID: m.MsgID, Observed: projection.Observed{
			ChannelID:   channelID,
			MsgID:       m.MsgID,
			MsgDate:     m.Date,
			IsDocument:  m.HasMedia,
			ActorUserID: m.FromID,
			RawText:     m.Text,
		}})
	}
	return out
}

func SortAscending(msgs []ParsedMessage) {
	slices.SortFunc(msgs, func(a, b ParsedMessage) int { return cmp.Compare(a.MsgID, b.MsgID) })
}
