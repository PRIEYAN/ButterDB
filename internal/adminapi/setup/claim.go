package setup

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/internal/storage/adminrepo"
)

const (
	maxDeviceNameRunes    = 64
	maxPlatformRunes      = 64
	maxClientVersionRunes = 32
)

func (s *Service) Claim(ctx context.Context, token string, device adminv1.DeviceInfo) (adminv1.ClaimResult, error) {
	info, err := validateDeviceInfo(device)
	if err != nil {
		return adminv1.ClaimResult{}, err
	}
	state, err := s.claims.ClaimState(ctx)
	if err != nil {
		return adminv1.ClaimResult{}, fmt.Errorf("setup: read claim state: %w", err)
	}
	if state != ClaimStateUnclaimed {
		return adminv1.ClaimResult{}, fmt.Errorf("setup: claim: %w", ErrAlreadyClaimed)
	}
	if !wellFormedSetupToken(token) {
		return adminv1.ClaimResult{}, fmt.Errorf("setup: claim: %w", ErrSetupTokenInvalid)
	}
	deviceToken, deviceID, tokenHash, err := s.tokens.NewDeviceToken()
	if err != nil {
		return adminv1.ClaimResult{}, fmt.Errorf("setup: issue device token: %w", err)
	}
	now := s.clock.Now().UTC().Truncate(time.Millisecond)
	record := adminrepo.NewDevice{
		DeviceID:      deviceID,
		Name:          info.Name,
		Platform:      info.Platform,
		ClientVersion: info.ClientVersion,
		CreatedVia:    adminrepo.CreatedViaSetup,
		TokenHash:     tokenHash,
		CreatedAt:     now,
		LastSeenIP:    info.RemoteAddr,
	}
	if err := s.claims.ConsumeSetupTokenAndAddDevice(ctx, hashSetupToken(token), now, record); err != nil {
		return adminv1.ClaimResult{}, s.claimFailure(ctx, err)
	}
	s.log.Info("server claimed", "device_id", deviceID)
	return adminv1.ClaimResult{
		Device: adminv1.Device{
			DeviceID:      deviceID,
			Name:          info.Name,
			Platform:      info.Platform,
			CreatedAt:     now,
			CreatedVia:    string(adminrepo.CreatedViaSetup),
			LastSeenAt:    now,
			LastSeenIP:    info.RemoteAddr,
			TokenIssuedAt: now,
			Current:       true,
		},
		DeviceToken: deviceToken,
		ServerID:    s.serverID,
	}, nil
}

func (s *Service) claimFailure(ctx context.Context, err error) error {
	if errors.Is(err, ErrAlreadyClaimed) || errors.Is(err, ErrSetupTokenInvalid) {
		return fmt.Errorf("setup: claim: %w", err)
	}
	if state, stateErr := s.claims.ClaimState(ctx); stateErr == nil && state != ClaimStateUnclaimed {
		return fmt.Errorf("setup: claim: %w", ErrAlreadyClaimed)
	}
	return fmt.Errorf("setup: claim: %w", err)
}

func validateDeviceInfo(device adminv1.DeviceInfo) (adminv1.DeviceInfo, error) {
	name := strings.TrimSpace(device.Name)
	if err := checkText("device_name", name, 1, maxDeviceNameRunes); err != nil {
		return adminv1.DeviceInfo{}, err
	}
	platform := strings.TrimSpace(device.Platform)
	if err := checkText("platform", platform, 0, maxPlatformRunes); err != nil {
		return adminv1.DeviceInfo{}, err
	}
	version := strings.TrimSpace(device.ClientVersion)
	if err := checkText("client_version", version, 0, maxClientVersionRunes); err != nil {
		return adminv1.DeviceInfo{}, err
	}
	return adminv1.DeviceInfo{Name: name, Platform: platform, ClientVersion: version, RemoteAddr: device.RemoteAddr}, nil
}

func checkText(field, value string, minRunes, maxRunes int) error {
	if !utf8.ValidString(value) {
		return &ValidationError{Field: field, Reason: "not valid utf-8"}
	}
	count := utf8.RuneCountInString(value)
	if count < minRunes {
		return &ValidationError{Field: field, Reason: "too short"}
	}
	if count > maxRunes {
		return &ValidationError{Field: field, Reason: "too long"}
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return &ValidationError{Field: field, Reason: "contains control characters"}
		}
	}
	return nil
}
