package faketg

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

const (
	defaultDocumentDC  = 2
	firstChannelID     = 1000000001
	firstDocumentID    = 5000000001
	historyPageMax     = 100
	deleteBatchSize    = 100
	rangeAlignment     = 4 * 1024
	rangeMaxBytes      = 1024 * 1024
	defaultCodeTimeout = 2 * time.Minute
	defaultCodeLife    = 5 * time.Minute
)

var (
	ErrInjectedTransport  = errors.New("faketg: engine forcibly closed: connection lost")
	ErrInjectedAccepted   = errors.New("faketg: send accepted but the response failed")
	ErrWriteForbidden     = errors.New("faketg: write forbidden")
	ErrPhoneNumberInvalid = errors.New("faketg: phone number invalid")
	ErrNoLoginScript      = errors.New("faketg: account has no login script")
	ErrInvalidFaultScript = errors.New("faketg: invalid fault script")
	ErrDuplicateAccount   = errors.New("faketg: duplicate account")
	ErrUnknownChannel     = errors.New("faketg: unknown channel")
	ErrInvalidArgument    = errors.New("faketg: invalid send")
)

type DelayFunc func(ctx context.Context, d time.Duration) error

type Option func(*Network) error

func WithDelay(fn DelayFunc) Option {
	return func(n *Network) error {
		if fn == nil {
			return fmt.Errorf("faketg: %w: delay", errs.ErrMissingDependency)
		}
		n.delay = fn
		return nil
	}
}

func WithDocumentDC(dcID int) Option {
	return func(n *Network) error {
		if dcID <= 0 {
			return fmt.Errorf("faketg: document dc must be positive")
		}
		n.documentDC = dcID
		return nil
	}
}

type Network struct {
	mu             sync.Mutex
	clock          clock.Clock
	delay          DelayFunc
	documentDC     int
	accounts       map[int64]*Client
	channels       map[int64]*channelState
	nextChannelID  int64
	nextDocumentID int64
}

func NewNetwork(clk clock.Clock, opts ...Option) (*Network, error) {
	if clk == nil {
		return nil, fmt.Errorf("faketg: %w: clock", errs.ErrMissingDependency)
	}
	n := &Network{
		clock:          clk,
		documentDC:     defaultDocumentDC,
		accounts:       make(map[int64]*Client),
		channels:       make(map[int64]*channelState),
		nextChannelID:  firstChannelID,
		nextDocumentID: firstDocumentID,
	}
	n.delay = n.clockDelay
	for _, opt := range opts {
		if opt == nil {
			return nil, fmt.Errorf("faketg: %w: option", errs.ErrMissingDependency)
		}
		if err := opt(n); err != nil {
			return nil, err
		}
	}
	return n, nil
}

func (n *Network) clockDelay(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if fake, ok := n.clock.(*clock.Fake); ok && d > 0 {
		fake.Advance(d)
	}
	return nil
}

func (n *Network) AddAccount(cfg AccountConfig) (*Client, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, exists := n.accounts[cfg.UserID]; exists {
		return nil, fmt.Errorf("faketg: %w: user %d", ErrDuplicateAccount, cfg.UserID)
	}
	c := newClient(n, cfg)
	n.accounts[cfg.UserID] = c
	return c, nil
}

func NewClient(clk clock.Clock, userID int64, opts ...Option) (*Client, error) {
	n, err := NewNetwork(clk, opts...)
	if err != nil {
		return nil, err
	}
	return n.AddAccount(AccountConfig{UserID: userID, FirstName: "Fake", Username: fmt.Sprintf("fake%d", userID)})
}

func (n *Network) nowUnix() int64 {
	return n.clock.Now().Unix()
}

func (n *Network) channelLocked(channelID int64) (*channelState, error) {
	ch, ok := n.channels[channelID]
	if !ok {
		return nil, fmt.Errorf("faketg: channel %d: %w", channelID, tgclient.ErrChannelUnavailable)
	}
	return ch, nil
}

func (n *Network) newChannelLocked(channelID int64, title, about string, owner *Client) *channelState {
	ch := newChannelState(channelID, title, about, owner.userID)
	ch.members[owner.userID] = true
	n.channels[channelID] = ch
	return ch
}

func (n *Network) SeedChannel(channelID int64, title, about string, owner *Client) (tgclient.InputPeer, error) {
	if channelID <= 0 || owner == nil {
		return tgclient.InputPeer{}, fmt.Errorf("faketg: %w: seed channel needs a positive id and an owner", ErrInvalidArgument)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, exists := n.channels[channelID]; exists {
		return tgclient.InputPeer{}, fmt.Errorf("faketg: channel %d already exists", channelID)
	}
	n.newChannelLocked(channelID, title, about, owner)
	if channelID >= n.nextChannelID {
		n.nextChannelID = channelID + 1
	}
	return peerFor(channelID, owner.userID), nil
}

func (n *Network) Grant(channelID int64, member *Client, canPost bool) error {
	if member == nil {
		return fmt.Errorf("faketg: %w: member", errs.ErrMissingDependency)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	ch, ok := n.channels[channelID]
	if !ok {
		return fmt.Errorf("faketg: %w: %d", ErrUnknownChannel, channelID)
	}
	ch.members[member.userID] = canPost
	return nil
}

func (n *Network) ChannelIDs() []int64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	ids := make([]int64, 0, len(n.channels))
	for id := range n.channels {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func peerFor(channelID, userID int64) tgclient.InputPeer {
	return tgclient.InputPeer{ChannelID: channelID, AccessHash: accessHashFor(channelID, userID)}
}

func accessHashFor(channelID, userID int64) int64 {
	return (channelID*1000003+userID*7919)&0x3fffffffffffffff | 1
}
