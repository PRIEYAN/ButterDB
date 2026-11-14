package channelrepo

import (
	"context"
	"fmt"
)

func (r *Repository) SetSyncCursorTx(ctx context.Context, tx Tx, channelID, lastSyncedMsg int64, pts int) error {
	if err := validateCursor(lastSyncedMsg, pts); err != nil {
		return fmt.Errorf("channelrepo: set sync cursor %d: %w", channelID, err)
	}
	err := execOne(ctx, tx, channelID,
		`UPDATE channels SET last_synced_msg = MAX(last_synced_msg, ?2), pts = ?3 WHERE channel_id = ?1`,
		channelID, lastSyncedMsg, pts)
	if err != nil {
		return fmt.Errorf("channelrepo: set sync cursor %d: %w", channelID, err)
	}
	return nil
}

func (r *Repository) MarkInitialSyncDoneTx(ctx context.Context, tx Tx, channelID, lastSyncedMsg int64, pts int) error {
	if err := validateCursor(lastSyncedMsg, pts); err != nil {
		return fmt.Errorf("channelrepo: mark initial sync done %d: %w", channelID, err)
	}
	err := execOne(ctx, tx, channelID,
		`UPDATE channels SET initial_sync_done = 1, last_synced_msg = MAX(last_synced_msg, ?2), pts = ?3 WHERE channel_id = ?1`,
		channelID, lastSyncedMsg, pts)
	if err != nil {
		return fmt.Errorf("channelrepo: mark initial sync done %d: %w", channelID, err)
	}
	return nil
}

func validateCursor(lastSyncedMsg int64, pts int) error {
	if lastSyncedMsg < 0 {
		return fmt.Errorf("%w: last synced message %d", ErrInvalidChannel, lastSyncedMsg)
	}
	if pts < 0 {
		return fmt.Errorf("%w: pts %d", ErrInvalidChannel, pts)
	}
	return nil
}
