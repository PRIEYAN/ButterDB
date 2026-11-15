package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/PRIEYAN/ButterDB/internal/buckets"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

type registryStateLoader interface {
	LoadRegistryState(ctx context.Context) (buckets.RegistryState, bool, error)
}

type primaryAccountProbe interface {
	HasPrimaryAccount(ctx context.Context) (bool, error)
}

type LocalStateDependencies struct {
	State       registryStateLoader
	Fingerprint string
	Database    databaseReader
	Accounts    primaryAccountProbe
}

type LocalStateChecker struct {
	deps LocalStateDependencies
}

func NewLocalStateChecker(deps LocalStateDependencies) (*LocalStateChecker, error) {
	switch {
	case deps.State == nil:
		return nil, fmt.Errorf("core: local state: %w: state", errs.ErrMissingDependency)
	case deps.Fingerprint == "":
		return nil, fmt.Errorf("core: local state: %w: key fingerprint", errs.ErrMissingDependency)
	case deps.Database == nil:
		return nil, fmt.Errorf("core: local state: %w: projection database", errs.ErrMissingDependency)
	case deps.Accounts == nil:
		return nil, fmt.Errorf("core: local state: %w: accounts", errs.ErrMissingDependency)
	}
	return &LocalStateChecker{deps: deps}, nil
}

func (c *LocalStateChecker) Check(ctx context.Context) error {
	state, found, err := c.deps.State.LoadRegistryState(ctx)
	if err != nil {
		return err
	}
	if !found {
		return CheckUnanchored(ctx, c.deps.Accounts)
	}
	if state.KeyFingerprint != c.deps.Fingerprint {
		return keyFailure(ReasonKEKMismatch, "the server key does not match the registry fingerprint")
	}
	return c.checkProjection(ctx, state)
}

func CheckUnanchored(ctx context.Context, accounts primaryAccountProbe) error {
	linked, err := accounts.HasPrimaryAccount(ctx)
	if err != nil {
		return fmt.Errorf("core: read primary account: %w", err)
	}
	if linked {
		return keyFailure(ReasonRegistryUnknown, "a primary account is linked but there is no state file; run butterdbd setup to rediscover the registry")
	}
	return nil
}

type markerRow struct {
	channelID   int64
	msgID       int64
	fingerprint string
	userID      int64
	minReader   int
}

func (c *LocalStateChecker) checkProjection(ctx context.Context, state buckets.RegistryState) error {
	marker, hasMarker, registryID, hasRegistry, err := c.readProjection(ctx)
	if err != nil {
		return fmt.Errorf("core: read projection for startup checks: %w", err)
	}
	if hasMarker {
		if marker.msgID != state.MarkerMsgID || marker.channelID != state.ChannelID ||
			marker.fingerprint != state.KeyFingerprint || marker.userID != state.PrimaryUserID {
			return keyFailure(ReasonRegistryMarkerMismatch, "the projected registry marker differs from the state file")
		}
		if marker.minReader > wire.FormatVersion {
			return incompatibleFailure(ReasonFormatTooNew, "the registry needs a newer binary")
		}
	}
	if hasRegistry && registryID != state.ChannelID {
		return incompatibleFailure(ReasonProjectionMismatch, "the projected registry channel differs from the state file; run butterdbd rebuild --from-telegram")
	}
	return nil
}

func (c *LocalStateChecker) readProjection(ctx context.Context) (marker markerRow, hasMarker bool, registryID int64, hasRegistry bool, err error) {
	err = c.deps.Database.Read(ctx, func(tx *sql.Tx) error {
		var readErr error
		marker, hasMarker, readErr = readCurrentMarker(ctx, tx)
		if readErr != nil {
			return readErr
		}
		registryID, hasRegistry, readErr = readRegistryChannel(ctx, tx)
		return readErr
	})
	return marker, hasMarker, registryID, hasRegistry, err
}

func readCurrentMarker(ctx context.Context, tx *sql.Tx) (markerRow, bool, error) {
	var row markerRow
	err := tx.QueryRowContext(ctx,
		`SELECT channel_id, msg_id, kek_fingerprint, primary_user_id, min_reader FROM registry_markers ORDER BY msg_id DESC LIMIT 1`).
		Scan(&row.channelID, &row.msgID, &row.fingerprint, &row.userID, &row.minReader)
	if errors.Is(err, sql.ErrNoRows) {
		return markerRow{}, false, nil
	}
	return row, err == nil, err
}

func readRegistryChannel(ctx context.Context, tx *sql.Tx) (int64, bool, error) {
	var channelID int64
	err := tx.QueryRowContext(ctx, `SELECT channel_id FROM channels WHERE kind = 'registry'`).Scan(&channelID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return channelID, err == nil, err
}

type primaryAlwaysLinked struct{}

func (primaryAlwaysLinked) HasPrimaryAccount(context.Context) (bool, error) {
	return true, nil
}
