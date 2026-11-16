package vm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

const (
	apiVersionV1        = "v1"
	deliverySavedMsgs   = "saved_messages"
	jsonTimeout         = 75 * time.Second
	tokenRotationAge    = 30 * 24 * time.Hour
	HostCommand         = "butterdbd admin-token"
	maxDeviceNameLength = 64
)

var ErrMissingDependency = errors.New("missing dependency")

type Dialer interface {
	Probe(ctx context.Context, baseURL string) (adminv1.CertificateInfo, error)
	Dial(cfg adminv1.ClientConfig) (ConnectClient, error)
}

type ConnectClient interface {
	ServerInfo(ctx context.Context) (adminv1.ServerInfo, error)
	Claim(ctx context.Context, req adminv1.ClaimRequest) (adminv1.DeviceCredentials, error)
	RequestPairing(ctx context.Context, req adminv1.PairingRequest) (adminv1.PairingRequested, error)
	CompletePairing(ctx context.Context, req adminv1.PairingCompleteRequest) (adminv1.DeviceCredentials, error)
	RotateDeviceToken(ctx context.Context) (adminv1.DeviceCredentials, error)
	SetToken(token string)
}

type TokenStores struct {
	Keychain model.TokenStore
	File     model.TokenStore
	Memory   model.TokenStore
}

type Dependencies struct {
	Dialer            Dialer
	Tokens            TokenStores
	Profiles          model.ProfileStore
	Now               func() time.Time
	OnConnected       func(model.Profile)
	DefaultDeviceName string
	Platform          string
	ClientVersion     string
	UserAgent         string
}

func (d Dependencies) validate() error {
	checks := []struct {
		name    string
		missing bool
	}{
		{"dialer", d.Dialer == nil},
		{"keychain", d.Tokens.Keychain == nil},
		{"file store", d.Tokens.File == nil},
		{"memory store", d.Tokens.Memory == nil},
		{"profiles", d.Profiles == nil},
		{"clock", d.Now == nil},
		{"on connected", d.OnConnected == nil},
	}
	for _, c := range checks {
		if c.missing {
			return fmt.Errorf("connect/vm: %w: %s", ErrMissingDependency, c.name)
		}
	}
	return nil
}

func (t TokenStores) forKind(kind model.TokenStorage) model.TokenStore {
	switch kind {
	case model.TokenFile:
		return t.File
	case model.TokenMemory:
		return t.Memory
	default:
		return t.Keychain
	}
}
