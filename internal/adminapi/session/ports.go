package session

import (
	"context"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/storage/adminrepo"
)

type DeviceStore interface {
	Device(ctx context.Context, id string) (adminrepo.Device, error)
	InsertDevice(ctx context.Context, d adminrepo.NewDevice) error
	Rotate(ctx context.Context, id string, newHash [32]byte, now time.Time) error
	MarkNewTokenUsed(ctx context.Context, id string) error
	Revoke(ctx context.Context, id string, now time.Time) error
	TouchSeen(ctx context.Context, id string, ip string, now time.Time) error
	RevokeIdle(ctx context.Context, before time.Time) (int, error)
	ReplacePairingCode(ctx context.Context, delivery string, hash [32]byte, expiresAt time.Time) error
	DeletePairingCode(ctx context.Context, delivery string) error
	ConsumePairingCode(ctx context.Context, hash [32]byte, now time.Time, d adminrepo.NewDevice) error
	RecordPairingFailure(ctx context.Context) (consecutive int, err error)
	InvalidatePairingCodes(ctx context.Context) error
}

type SavedMessagesSender interface {
	SendToSavedMessages(ctx context.Context, text string) error
}

type AuditWriter interface {
	InsertAudit(ctx context.Context, e adminrepo.AuditEntry) (int64, error)
}

type claimStateReader interface {
	ClaimState(ctx context.Context) (string, error)
}
