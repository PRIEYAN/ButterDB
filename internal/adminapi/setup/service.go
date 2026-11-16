package setup

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/storage/adminrepo"
)

const (
	ClaimStateUnclaimed = "unclaimed"
	ClaimStateClaimed   = "claimed"
	DefaultTTL          = 24 * time.Hour
)

type ClaimStore interface {
	ClaimState(ctx context.Context) (string, error)
	ReplaceSetupToken(ctx context.Context, hash [32]byte, expiresAt time.Time) error
	ConsumeSetupTokenAndAddDevice(ctx context.Context, hash [32]byte, now time.Time, dev adminrepo.NewDevice) error
}

type DeviceIssuer interface {
	NewDeviceToken() (token string, deviceID string, hash [32]byte, err error)
}

type Dependencies struct {
	Relay    LoginRelay
	Claims   ClaimStore
	Tokens   DeviceIssuer
	Clock    clock.Clock
	Random   io.Reader
	Logger   *slog.Logger
	TTL      time.Duration
	ServerID string
}

type Service struct {
	relay    LoginRelay
	claims   ClaimStore
	tokens   DeviceIssuer
	clock    clock.Clock
	random   io.Reader
	log      *slog.Logger
	ttl      time.Duration
	serverID string
}

func New(deps Dependencies) (*Service, error) {
	if err := validateDependencies(deps); err != nil {
		return nil, err
	}
	ttl := deps.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Service{
		relay:    deps.Relay,
		claims:   deps.Claims,
		tokens:   deps.Tokens,
		clock:    deps.Clock,
		random:   deps.Random,
		log:      deps.Logger,
		ttl:      ttl,
		serverID: deps.ServerID,
	}, nil
}

func validateDependencies(deps Dependencies) error {
	missing := []struct {
		name string
		nil  bool
	}{
		{"relay", deps.Relay == nil},
		{"claims", deps.Claims == nil},
		{"tokens", deps.Tokens == nil},
		{"clock", deps.Clock == nil},
		{"random", deps.Random == nil},
		{"logger", deps.Logger == nil},
		{"server id", deps.ServerID == ""},
	}
	for _, dep := range missing {
		if dep.nil {
			return fmt.Errorf("setup: %w: %s", errs.ErrMissingDependency, dep.name)
		}
	}
	return nil
}
