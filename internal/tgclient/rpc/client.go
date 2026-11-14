package rpc

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/scope"
)

const (
	channelsGetMessagesLimit = 100
	deleteMessagesBatchSize  = 100
	historyPageLimit         = 100
	differencePageLimit      = 100
	ownedDialogsBatchSize    = 100
)

type Dependencies struct {
	Invoker scope.Invoker
	Gate    tgclient.WriteGate
	Random  io.Reader
}

type Client struct {
	invoker scope.Invoker
	gate    tgclient.WriteGate
	random  io.Reader
}

var _ tgclient.ChannelRPC = (*Client)(nil)

func New(deps Dependencies) (*Client, error) {
	if deps.Invoker == nil {
		return nil, fmt.Errorf("rpc: %w: invoker", errs.ErrMissingDependency)
	}
	if deps.Gate == nil {
		return nil, fmt.Errorf("rpc: %w: gate", errs.ErrMissingDependency)
	}
	if deps.Random == nil {
		return nil, fmt.Errorf("rpc: %w: random", errs.ErrMissingDependency)
	}
	return &Client{invoker: deps.Invoker, gate: deps.Gate, random: deps.Random}, nil
}

func (c *Client) read(ctx context.Context, operation string, fn func(ctx context.Context, api *tg.Client) error) error {
	return mapError(operation, c.invoker.Invoke(ctx, fn))
}

func (c *Client) write(ctx context.Context, class tgclient.WriteClass, operation string, fn func(ctx context.Context, api *tg.Client) error) error {
	return c.gate.Do(ctx, class, func() error {
		return c.read(ctx, operation, fn)
	})
}

func requirePeer(operation string, peer tgclient.InputPeer) error {
	if peer.ChannelID <= 0 {
		return fmt.Errorf("rpc: %s: %w: invalid channel id", operation, tgclient.ErrChannelUnavailable)
	}
	if peer.AccessHash == 0 {
		return fmt.Errorf("rpc: %s: %w", operation, tgclient.ErrZeroAccessHash)
	}
	return nil
}

func inputChannel(peer tgclient.InputPeer) *tg.InputChannel {
	return &tg.InputChannel{ChannelID: peer.ChannelID, AccessHash: peer.AccessHash}
}

func inputPeerChannel(peer tgclient.InputPeer) *tg.InputPeerChannel {
	return &tg.InputPeerChannel{ChannelID: peer.ChannelID, AccessHash: peer.AccessHash}
}

func mapError(operation string, err error) error {
	if err == nil {
		return nil
	}
	var flood *tgclient.FloodWaitError
	if errors.As(err, &flood) {
		return fmt.Errorf("rpc: %s: %w", operation, err)
	}
	if wait, ok := tgerr.AsFloodWait(err); ok {
		return fmt.Errorf("rpc: %s: %w", operation, &tgclient.FloodWaitError{Duration: wait})
	}
	switch {
	case tgerr.Is(err, "CHANNEL_PRIVATE", "CHANNEL_INVALID"):
		return fmt.Errorf("rpc: %s: %w", operation, tgclient.ErrChannelUnavailable)
	case tgerr.Is(err, "AUTH_KEY_UNREGISTERED", "SESSION_REVOKED", "SESSION_EXPIRED", "USER_DEACTIVATED"):
		return fmt.Errorf("rpc: %s: %w", operation, tgclient.ErrNotAuthorized)
	}
	return fmt.Errorf("rpc: %s: %w", operation, err)
}

func isDefiniteRejection(err error) bool {
	if _, ok := tgerr.As(err); ok {
		return true
	}
	var flood *tgclient.FloodWaitError
	return errors.As(err, &flood)
}
