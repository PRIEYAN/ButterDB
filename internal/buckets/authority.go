package buckets

import (
	"context"
	"fmt"
)

func markChannelEmptyAuthoritative(ctx context.Context, channels ChannelRecords, gate ChannelGate, scanner EmptyChannelSyncer, channelID int64) error {
	current, found, err := channels.Channel(ctx, channelID)
	if err != nil {
		return fmt.Errorf("buckets: load channel %d: %w", channelID, err)
	}
	if found && current.InitialSyncDone {
		return nil
	}
	release, err := gate.Acquire(ctx, channelID)
	if err != nil {
		return fmt.Errorf("buckets: gate channel %d: %w", channelID, err)
	}
	defer release()
	if err := scanner.InitialSyncEmptyChannel(ctx, channelID); err != nil {
		return fmt.Errorf("buckets: mark channel %d authoritative: %w", channelID, err)
	}
	return nil
}
