package vm

import (
	"context"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
)

type LoginFlow interface {
	Status(ctx context.Context) (adminv1.TelegramStatus, error)
	SaveApp(ctx context.Context, req adminv1.TelegramCredentialsRequest) (adminv1.TelegramStatus, error)
	DeleteApp(ctx context.Context) (adminv1.TelegramStatus, error)
	Start(ctx context.Context, phone string) (adminv1.TelegramStatus, error)
	SubmitCode(ctx context.Context, loginID, code string) (adminv1.TelegramStatus, error)
	SubmitPassword(ctx context.Context, loginID, password string) (adminv1.TelegramStatus, error)
	Cancel(ctx context.Context) (adminv1.TelegramStatus, error)
}
