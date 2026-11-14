package apply

import (
	"context"
	"database/sql"
	"errors"

	"github.com/PRIEYAN/ButterDB/internal/projection"
)

const writerRekey = "sys.rekey"

type markerApplier struct{}

type markerRow struct {
	MsgID         int64
	DeploymentID  string
	FormatVersion int
	MinReader     int
	Fingerprint   string
	PrimaryUserID int64
}

func (markerApplier) Apply(ctx context.Context, tx projection.Tx, in projection.ApplyInput) (projection.ApplyResult, error) {
	if err := requireRegistryScope(ctx, tx, in); err != nil {
		return projection.ApplyResult{}, err
	}
	channelID := in.Observed.ChannelID
	recorded, err := isMarkerRecorded(ctx, tx, channelID, in.Observed.MsgID)
	if err != nil || recorded {
		return applied(), err
	}
	previous, found, err := loadPrecedingMarker(ctx, tx, channelID, in.Observed.MsgID)
	if err != nil {
		return projection.ApplyResult{}, err
	}
	if err := checkMarkerRevision(ctx, tx, in, previous, found); err != nil {
		return projection.ApplyResult{}, err
	}
	return applied(), insertMarker(ctx, tx, in)
}

func isMarkerRecorded(ctx context.Context, tx projection.Tx, channelID, msgID int64) (bool, error) {
	var present int
	err := tx.QueryRowContext(ctx,
		`SELECT 1 FROM registry_markers WHERE channel_id = ?1 AND msg_id = ?2`, channelID, msgID).Scan(&present)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, sqlFailure("load registry marker", err)
	}
	return true, nil
}

func loadPrecedingMarker(ctx context.Context, tx projection.Tx, channelID, msgID int64) (markerRow, bool, error) {
	var row markerRow
	err := tx.QueryRowContext(ctx,
		`SELECT msg_id, deployment_id, format_version, min_reader, kek_fingerprint, primary_user_id
FROM registry_markers WHERE channel_id = ?1 AND msg_id < ?2 ORDER BY msg_id DESC LIMIT 1`,
		channelID, msgID).Scan(&row.MsgID, &row.DeploymentID, &row.FormatVersion, &row.MinReader, &row.Fingerprint, &row.PrimaryUserID)
	if errors.Is(err, sql.ErrNoRows) {
		return markerRow{}, false, nil
	}
	if err != nil {
		return markerRow{}, false, sqlFailure("load preceding registry marker", err)
	}
	return row, true, nil
}

func checkMarkerRevision(ctx context.Context, tx projection.Tx, in projection.ApplyInput, previous markerRow, hasPrevious bool) error {
	if !hasPrevious {
		return requireNoMarkers(ctx, tx, in.Observed.ChannelID)
	}
	op := in.Op
	if op.DeploymentID != previous.DeploymentID || op.PrimaryUserID != previous.PrimaryUserID {
		return rejectf(projection.RejectMarkerConflict, "marker names another deployment or user")
	}
	if op.FormatVersion < previous.FormatVersion || op.MinReaderVersion < previous.MinReader {
		return rejectf(projection.RejectMarkerConflict, "marker lowers the format version")
	}
	if op.KEKFingerprint != previous.Fingerprint && op.Writer != writerRekey {
		return rejectf(projection.RejectMarkerConflict, "marker changes the key fingerprint without a rekey writer")
	}
	return nil
}

func requireNoMarkers(ctx context.Context, tx projection.Tx, channelID int64) error {
	var count int
	err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM registry_markers WHERE channel_id = ?1`, channelID).Scan(&count)
	if err != nil {
		return sqlFailure("count registry markers", err)
	}
	if count > 0 {
		return rejectf(projection.RejectMarkerConflict, "marker precedes the deployment identity")
	}
	return nil
}

func insertMarker(ctx context.Context, tx projection.Tx, in projection.ApplyInput) error {
	op := in.Op
	_, err := tx.ExecContext(ctx,
		`INSERT INTO registry_markers (channel_id, msg_id, deployment_id, format_version, min_reader, kek_fingerprint, primary_user_id, writer, msg_date)
VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9)`,
		in.Observed.ChannelID, in.Observed.MsgID, op.DeploymentID, op.FormatVersion, op.MinReaderVersion,
		op.KEKFingerprint, op.PrimaryUserID, op.Writer, in.Observed.MsgDate)
	if err != nil {
		return sqlFailure("insert registry marker", err)
	}
	return nil
}
