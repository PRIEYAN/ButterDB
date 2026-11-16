package core

import (
	"context"
	"fmt"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/internal/adminapi/handlers"
	"github.com/PRIEYAN/ButterDB/internal/adminapi/session"
	"github.com/PRIEYAN/ButterDB/internal/adminapi/setup"
	"github.com/PRIEYAN/ButterDB/internal/identity"
)

var (
	_ handlers.PairingEndpoints   = (*pairingEndpoints)(nil)
	_ session.SavedMessagesSender = savedMessagesDelivery{}
)

type SavedMessagesSender interface {
	SendToSavedMessages(ctx context.Context, text string) (int64, error)
}

type savedMessagesDelivery struct {
	relay  setup.LoginRelay
	sender SavedMessagesSender
}

func (d savedMessagesDelivery) SendToSavedMessages(ctx context.Context, text string) error {
	snap, err := d.relay.Status(ctx)
	if err != nil {
		return fmt.Errorf("core: pairing: telegram status: %w", err)
	}
	if snap.State != identity.StateLinked {
		return fmt.Errorf("core: pairing: %w: telegram is not linked", session.ErrPairingUnavailable)
	}
	if _, err := d.sender.SendToSavedMessages(ctx, text); err != nil {
		return fmt.Errorf("core: pairing: send to saved messages: %w", err)
	}
	return nil
}

type telegramSavedMessages struct {
	telegram *Telegram
}

func (t telegramSavedMessages) SendToSavedMessages(ctx context.Context, text string) (int64, error) {
	transport, err := t.telegram.Transport(ctx)
	if err != nil {
		return 0, err
	}
	return transport.Channels.SendToSavedMessages(ctx, text)
}

type pairingEndpoints struct {
	pairing  *session.Pairing
	limiter  *session.Limiter
	serverID string
}

func newPairingEndpoints(pairing *session.Pairing, limiter *session.Limiter, serverID string) *pairingEndpoints {
	return &pairingEndpoints{pairing: pairing, limiter: limiter, serverID: serverID}
}

func (p *pairingEndpoints) Request(ctx context.Context, delivery string, device adminv1.DeviceInfo) (time.Time, error) {
	if err := p.limiter.Take(session.ScopePairingRequestAddr, device.RemoteAddr); err != nil {
		return time.Time{}, translateAdminError(err)
	}
	expiresAt, err := p.pairing.Request(ctx, delivery, device)
	if err != nil {
		return time.Time{}, translateAdminError(err)
	}
	return expiresAt, nil
}

func (p *pairingEndpoints) Complete(ctx context.Context, code string, device adminv1.DeviceInfo) (adminv1.DeviceTokenResult, error) {
	if err := p.limiter.Take(session.ScopePairingComplete, device.RemoteAddr); err != nil {
		return adminv1.DeviceTokenResult{}, translateAdminError(err)
	}
	result, err := p.pairing.Complete(ctx, code, device)
	if err != nil {
		return adminv1.DeviceTokenResult{}, translateAdminError(err)
	}
	result.ServerID = p.serverID
	return result, nil
}
