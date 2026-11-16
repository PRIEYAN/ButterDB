package core

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/adminapi/handlers"
	"github.com/PRIEYAN/ButterDB/internal/adminapi/session"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/storage/adminrepo"
)

const revokedDeviceListing = 30 * 24 * time.Hour

var _ handlers.DeviceService = (*deviceService)(nil)

type deviceRepository interface {
	Device(ctx context.Context, id string) (adminrepo.Device, error)
	ListDevices(ctx context.Context, includeRevokedSince time.Time) ([]adminrepo.Device, error)
	ActiveDeviceCount(ctx context.Context) (int, error)
	RenameDevice(ctx context.Context, id, name string) error
	Rotate(ctx context.Context, id string, newHash [32]byte, now time.Time) error
	Revoke(ctx context.Context, id string, now time.Time) error
}

type deviceService struct {
	repo   deviceRepository
	tokens *session.Tokens
	clock  clock.Clock

	revokeMu sync.Mutex
}

func newDeviceService(repo deviceRepository, tokens *session.Tokens, clk clock.Clock) *deviceService {
	return &deviceService{repo: repo, tokens: tokens, clock: clk}
}

func (d *deviceService) List(ctx context.Context) ([]adminrepo.Device, error) {
	return d.repo.ListDevices(ctx, d.clock.Now().Add(-revokedDeviceListing))
}

func (d *deviceService) Rename(ctx context.Context, deviceID, name string) (adminrepo.Device, error) {
	if err := d.repo.RenameDevice(ctx, deviceID, name); err != nil {
		return adminrepo.Device{}, translateDeviceError(err)
	}
	return d.load(ctx, deviceID)
}

func (d *deviceService) Revoke(ctx context.Context, deviceID string, confirmLast bool) error {
	d.revokeMu.Lock()
	defer d.revokeMu.Unlock()
	device, err := d.load(ctx, deviceID)
	if err != nil {
		return err
	}
	if device.RevokedAt.IsZero() && !confirmLast {
		if err := d.requireAnotherActive(ctx); err != nil {
			return err
		}
	}
	if err := d.repo.Revoke(ctx, deviceID, d.clock.Now()); err != nil {
		return translateDeviceError(err)
	}
	return nil
}

func (d *deviceService) requireAnotherActive(ctx context.Context) error {
	active, err := d.repo.ActiveDeviceCount(ctx)
	if err != nil {
		return err
	}
	if active <= 1 {
		return fmt.Errorf("core: revoke device: %w", handlers.ErrLastDevice)
	}
	return nil
}

func (d *deviceService) Rotate(ctx context.Context, deviceID string) (adminrepo.Device, string, error) {
	token, err := d.rotatedToken(deviceID)
	if err != nil {
		return adminrepo.Device{}, "", err
	}
	if err := d.repo.Rotate(ctx, deviceID, session.HashSecret(session.KindDevice, token), d.clock.Now()); err != nil {
		return adminrepo.Device{}, "", translateDeviceError(err)
	}
	device, err := d.load(ctx, deviceID)
	if err != nil {
		return adminrepo.Device{}, "", err
	}
	return device, token, nil
}

func (d *deviceService) rotatedToken(deviceID string) (string, error) {
	minted, _, _, err := d.tokens.NewDeviceToken()
	if err != nil {
		return "", fmt.Errorf("core: rotate device token: %w", err)
	}
	mintedID, err := session.ParseDeviceToken(minted)
	if err != nil {
		return "", fmt.Errorf("core: rotate device token: %w", err)
	}
	secret := minted[len(deviceTokenPrefix)+len(mintedID):]
	return deviceTokenPrefix + deviceID + secret, nil
}

func (d *deviceService) load(ctx context.Context, deviceID string) (adminrepo.Device, error) {
	device, err := d.repo.Device(ctx, deviceID)
	if err != nil {
		return adminrepo.Device{}, translateDeviceError(err)
	}
	return device, nil
}

const deviceTokenPrefix = "bdt1_"

func translateDeviceError(err error) error {
	if errors.Is(err, adminrepo.ErrDeviceNotFound) {
		return fmt.Errorf("%w: %w", handlers.ErrDeviceNotFound, err)
	}
	return err
}
