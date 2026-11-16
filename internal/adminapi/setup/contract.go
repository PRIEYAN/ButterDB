package setup

import (
	"context"

	"github.com/PRIEYAN/ButterDB/internal/identity"
)

type LoginRelay interface {
	Status(ctx context.Context) (identity.Snapshot, error)
	SaveCredentials(ctx context.Context, apiID int, apiHash string) error
	DeleteCredentials(ctx context.Context) error
	Start(ctx context.Context, phone string) (identity.Snapshot, error)
	SubmitCode(ctx context.Context, loginID, code string) (identity.Snapshot, error)
	SubmitPassword(ctx context.Context, loginID, password string) (identity.Snapshot, error)
	Cancel(ctx context.Context) (identity.Snapshot, error)
	Unlink(ctx context.Context) (identity.Snapshot, error)
}
