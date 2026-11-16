package app

import (
	"context"
	"fmt"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
)

type telegramAPI interface {
	TelegramStatus(ctx context.Context) (adminv1.TelegramStatus, error)
	SaveTelegramCredentials(ctx context.Context, req adminv1.TelegramCredentialsRequest) (adminv1.TelegramStatus, error)
	DeleteTelegramCredentials(ctx context.Context) (adminv1.TelegramStatus, error)
	StartTelegramLogin(ctx context.Context, req adminv1.TelegramLoginStartRequest) (adminv1.TelegramStatus, error)
	SubmitTelegramCode(ctx context.Context, req adminv1.TelegramCodeRequest) (adminv1.TelegramStatus, error)
	SubmitTelegramPassword(ctx context.Context, req adminv1.TelegramPasswordRequest) (adminv1.TelegramStatus, error)
	CancelTelegramLogin(ctx context.Context) (adminv1.TelegramStatus, error)
}

type primaryFlow struct {
	api telegramAPI
}

func newPrimaryFlow(api telegramAPI) (*primaryFlow, error) {
	if api == nil {
		return nil, fmt.Errorf("app: %w: telegram api", ErrMissingDependency)
	}
	return &primaryFlow{api: api}, nil
}

func (f *primaryFlow) Status(ctx context.Context) (adminv1.TelegramStatus, error) {
	return f.api.TelegramStatus(ctx)
}

func (f *primaryFlow) SaveApp(ctx context.Context, req adminv1.TelegramCredentialsRequest) (adminv1.TelegramStatus, error) {
	return f.api.SaveTelegramCredentials(ctx, req)
}

func (f *primaryFlow) DeleteApp(ctx context.Context) (adminv1.TelegramStatus, error) {
	return f.api.DeleteTelegramCredentials(ctx)
}

func (f *primaryFlow) Start(ctx context.Context, phone string) (adminv1.TelegramStatus, error) {
	return f.api.StartTelegramLogin(ctx, adminv1.TelegramLoginStartRequest{Phone: phone})
}

func (f *primaryFlow) SubmitCode(ctx context.Context, loginID, code string) (adminv1.TelegramStatus, error) {
	return f.api.SubmitTelegramCode(ctx, adminv1.TelegramCodeRequest{LoginID: loginID, Code: code})
}

func (f *primaryFlow) SubmitPassword(ctx context.Context, loginID, password string) (adminv1.TelegramStatus, error) {
	return f.api.SubmitTelegramPassword(ctx, adminv1.TelegramPasswordRequest{LoginID: loginID, Password: password})
}

func (f *primaryFlow) Cancel(ctx context.Context) (adminv1.TelegramStatus, error) {
	return f.api.CancelTelegramLogin(ctx)
}
