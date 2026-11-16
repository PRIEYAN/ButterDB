package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/storage/adminrepo"
)

const (
	PairingCodeTTL          = 10 * time.Minute
	PairingFailureLimit     = 5
	SavedMessagesSendLimit  = 5
	SavedMessagesSendWindow = time.Hour
	DeliverySavedMessages   = "saved_messages"
	DeliveryHost            = "host"

	pairingCodeAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	pairingCodeLength   = 10
	pairingCodeGroup    = 5
)

type Pairing struct {
	store  DeviceStore
	sender SavedMessagesSender
	tokens *Tokens
	clock  clock.Clock
	random io.Reader

	mu    sync.Mutex
	sends []time.Time
}

func NewPairing(store DeviceStore, sender SavedMessagesSender, tokens *Tokens, clock clock.Clock, random io.Reader) *Pairing {
	return &Pairing{store: store, sender: sender, tokens: tokens, clock: clock, random: random}
}

func NormalisePairingCode(input string) (string, error) {
	var b strings.Builder
	for _, r := range strings.ToUpper(input) {
		switch {
		case r == '-' || unicode.IsSpace(r):
			continue
		case r == 'O':
			r = '0'
		case r == 'I' || r == 'L':
			r = '1'
		}
		b.WriteRune(r)
	}
	code := b.String()
	if len(code) != pairingCodeLength || strings.Trim(code, pairingCodeAlphabet) != "" {
		return "", fmt.Errorf("session: %w", ErrPairingCodeInvalid)
	}
	return code, nil
}

func formatPairingCode(code string) string {
	return code[:pairingCodeGroup] + "-" + code[pairingCodeGroup:]
}

func (p *Pairing) ready() error {
	switch {
	case p == nil || p.store == nil:
		return fmt.Errorf("session: %w: device store", errs.ErrMissingDependency)
	case p.tokens == nil:
		return fmt.Errorf("session: %w: tokens", errs.ErrMissingDependency)
	case p.clock == nil:
		return fmt.Errorf("session: %w: clock", errs.ErrMissingDependency)
	case p.random == nil:
		return fmt.Errorf("session: %w: random", errs.ErrMissingDependency)
	}
	return nil
}

func (p *Pairing) mintCode() (string, error) {
	var raw [pairingCodeLength]byte
	if _, err := io.ReadFull(p.random, raw[:]); err != nil {
		return "", fmt.Errorf("session: read randomness: %w", err)
	}
	code := make([]byte, pairingCodeLength)
	for i, b := range raw {
		code[i] = pairingCodeAlphabet[b&31]
	}
	return string(code), nil
}

func (p *Pairing) requireClaimed(ctx context.Context) error {
	reader, ok := p.store.(claimStateReader)
	if !ok {
		return nil
	}
	state, err := reader.ClaimState(ctx)
	if err != nil {
		return fmt.Errorf("session: claim state: %w", err)
	}
	if state != adminrepo.ClaimStateClaimed {
		return ErrNotClaimed
	}
	return nil
}

func (p *Pairing) reserveSend() error {
	now := p.clock.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	kept := p.sends[:0]
	for _, at := range p.sends {
		if now.Sub(at) < SavedMessagesSendWindow {
			kept = append(kept, at)
		}
	}
	p.sends = kept
	if len(p.sends) >= SavedMessagesSendLimit {
		return &RateLimitedError{Scope: ScopePairingRequest, RetryAfter: p.sends[0].Add(SavedMessagesSendWindow).Sub(now)}
	}
	p.sends = append(p.sends, now)
	return nil
}

func (p *Pairing) Request(ctx context.Context, delivery string, device adminv1.DeviceInfo) (time.Time, error) {
	if err := p.ready(); err != nil {
		return time.Time{}, err
	}
	if p.sender == nil {
		return time.Time{}, fmt.Errorf("session: %w: saved messages sender", errs.ErrMissingDependency)
	}
	if delivery != DeliverySavedMessages {
		return time.Time{}, ErrUnsupportedDelivery
	}
	if err := p.requireClaimed(ctx); err != nil {
		return time.Time{}, err
	}
	if err := p.reserveSend(); err != nil {
		return time.Time{}, err
	}
	code, err := p.mintCode()
	if err != nil {
		return time.Time{}, err
	}
	expiresAt := p.clock.Now().Add(PairingCodeTTL)
	if err := p.store.ReplacePairingCode(ctx, DeliverySavedMessages, HashSecret(KindPairing, code), expiresAt); err != nil {
		return time.Time{}, fmt.Errorf("session: store pairing code: %w", err)
	}
	if err := p.sender.SendToSavedMessages(ctx, savedMessagesText(code, device, expiresAt)); err != nil {
		cleanup := context.WithoutCancel(ctx)
		if deleteErr := p.store.DeletePairingCode(cleanup, DeliverySavedMessages); deleteErr != nil {
			err = errors.Join(err, fmt.Errorf("session: delete pairing code: %w", deleteErr))
		}
		return time.Time{}, fmt.Errorf("session: send pairing code: %w", err)
	}
	return expiresAt, nil
}

func savedMessagesText(code string, device adminv1.DeviceInfo, expiresAt time.Time) string {
	return fmt.Sprintf("butterDB pairing code: %s\nDevice: \"%s\" (%s)\nExpires: %s UTC\nIf you did not request this, ignore it. Your server is still safe.",
		formatPairingCode(code), printable(device.Name), printable(device.Platform), expiresAt.UTC().Format("2006-01-02 15:04"))
}

func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '"' {
			return ' '
		}
		return r
	}, s)
}

func (p *Pairing) HostCode(ctx context.Context) (string, time.Time, error) {
	if err := p.ready(); err != nil {
		return "", time.Time{}, err
	}
	if err := p.requireClaimed(ctx); err != nil {
		return "", time.Time{}, err
	}
	code, err := p.mintCode()
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt := p.clock.Now().Add(PairingCodeTTL)
	if err := p.store.ReplacePairingCode(ctx, DeliveryHost, HashSecret(KindPairing, code), expiresAt); err != nil {
		return "", time.Time{}, fmt.Errorf("session: store pairing code: %w", err)
	}
	return formatPairingCode(code), expiresAt, nil
}

func (p *Pairing) Complete(ctx context.Context, code string, device adminv1.DeviceInfo) (adminv1.DeviceTokenResult, error) {
	if err := p.ready(); err != nil {
		return adminv1.DeviceTokenResult{}, err
	}
	normalised, err := NormalisePairingCode(code)
	if err != nil {
		return adminv1.DeviceTokenResult{}, p.failCompletion(ctx, err)
	}
	token, deviceID, tokenHash, err := p.tokens.NewDeviceToken()
	if err != nil {
		return adminv1.DeviceTokenResult{}, err
	}
	now := p.clock.Now()
	err = p.store.ConsumePairingCode(ctx, HashSecret(KindPairing, normalised), now, adminrepo.NewDevice{
		DeviceID:      deviceID,
		Name:          device.Name,
		Platform:      device.Platform,
		ClientVersion: device.ClientVersion,
		TokenHash:     tokenHash,
		CreatedAt:     now,
		LastSeenIP:    device.RemoteAddr,
	})
	switch {
	case errors.Is(err, adminrepo.ErrNotClaimed):
		return adminv1.DeviceTokenResult{}, ErrNotClaimed
	case errors.Is(err, adminrepo.ErrPairingCodeInvalid):
		return adminv1.DeviceTokenResult{}, p.failCompletion(ctx, ErrPairingCodeInvalid)
	case err != nil:
		return adminv1.DeviceTokenResult{}, fmt.Errorf("session: consume pairing code: %w", err)
	}
	stored, err := p.store.Device(ctx, deviceID)
	if err != nil {
		return adminv1.DeviceTokenResult{}, fmt.Errorf("session: load paired device: %w", err)
	}
	return adminv1.DeviceTokenResult{Device: DeviceDTO(stored, true), DeviceToken: token}, nil
}

func (p *Pairing) failCompletion(ctx context.Context, cause error) error {
	consecutive, err := p.store.RecordPairingFailure(ctx)
	if err != nil {
		return fmt.Errorf("session: record pairing failure: %w", err)
	}
	if consecutive >= PairingFailureLimit {
		if err := p.store.InvalidatePairingCodes(ctx); err != nil {
			return fmt.Errorf("session: invalidate pairing codes: %w", err)
		}
	}
	return cause
}
