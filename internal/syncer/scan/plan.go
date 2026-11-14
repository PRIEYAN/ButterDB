package scan

import (
	"context"
	"fmt"

	"github.com/PRIEYAN/ButterDB/internal/syncer"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

type historyPlan struct {
	upperBounds []int64
	highestSeen int64
}

func (s *Scanner) planHistory(ctx context.Context, channelID int64, peer tgclient.InputPeer, minID int64) (historyPlan, error) {
	var lowestPerPage []int64
	highestSeen := minID
	offsetID := int64(0)
	for {
		page, err := s.getHistory(ctx, channelID, peer, offsetID)
		if err != nil {
			return historyPlan{}, fmt.Errorf("scan: get history: %w", err)
		}
		if len(page) == 0 {
			break
		}
		lowest, newest := pageExtent(page)
		if newest > minID {
			lowestPerPage = append(lowestPerPage, lowest)
			highestSeen = max(highestSeen, newest)
		}
		// Only an empty page proves exhaustion; a short page is thinned by deletions, so the one exception is a page that reaches the watermark.
		if offsetID != 0 && lowest >= offsetID {
			return historyPlan{}, fmt.Errorf("scan: channel %d offset %d: %w", channelID, offsetID, syncer.ErrHistoryNoProgress)
		}
		if minID > 0 && lowest <= minID {
			break
		}
		offsetID = lowest
	}
	return historyPlan{upperBounds: upperBoundsOf(lowestPerPage, highestSeen), highestSeen: highestSeen}, nil
}

func pageExtent(page []tgclient.HistoryMessage) (lowest, newest int64) {
	lowest = page[0].MsgID
	for _, m := range page {
		lowest = min(lowest, m.MsgID)
		newest = max(newest, m.MsgID)
	}
	return lowest, newest
}

func upperBoundsOf(lowestPerPage []int64, highestSeen int64) []int64 {
	if len(lowestPerPage) == 0 {
		return nil
	}
	bounds := make([]int64, len(lowestPerPage))
	bounds[0] = highestSeen + 1
	for i := 1; i < len(lowestPerPage); i++ {
		bounds[i] = lowestPerPage[i-1]
	}
	return bounds
}

func filterHistoryPage(page []tgclient.HistoryMessage, minID, highestSeen int64) ([]tgclient.HistoryMessage, int64) {
	kept := make([]tgclient.HistoryMessage, 0, len(page))
	watermark := minID
	for _, m := range page {
		if m.MsgID <= minID || m.MsgID > highestSeen {
			continue
		}
		kept = append(kept, m)
		watermark = max(watermark, m.MsgID)
	}
	return kept, watermark
}
