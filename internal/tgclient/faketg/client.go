package faketg

import (
	"context"
	"fmt"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

var (
	_ tgclient.Sender      = (*Client)(nil)
	_ tgclient.RangeReader = (*Client)(nil)
	_ tgclient.ChannelRPC  = (*Client)(nil)
	_ tgclient.AuthRPC     = (*Client)(nil)
)

type LoginScript struct {
	Phone        string
	Code         string
	Password     string
	PasswordHint string
	CodeTimeout  time.Duration
	CodeLifetime time.Duration
}

type AccountConfig struct {
	UserID    int64
	FirstName string
	Username  string
	Login     *LoginScript
}

func (cfg AccountConfig) validate() error {
	if cfg.UserID <= 0 {
		return fmt.Errorf("faketg: %w: user id must be positive", ErrInvalidArgument)
	}
	if cfg.Login != nil && (cfg.Login.Phone == "" || cfg.Login.Code == "") {
		return fmt.Errorf("faketg: %w: login script needs a phone and a code", ErrInvalidArgument)
	}
	return nil
}

type SentSaved struct {
	MsgID int64
	Text  string
	Date  int64
}

type Client struct {
	n       *Network
	userID  int64
	profile tgclient.Account
	script  script

	login           *LoginScript
	authorized      bool
	pendingPassword bool
	codeSeq         int
	codeHash        string
	codeIssuedAt    time.Time

	saved        []SentSaved
	nextSavedID  int64
	readCalls    int
	cdnRedirects int
}

func newClient(n *Network, cfg AccountConfig) *Client {
	c := &Client{
		n:           n,
		userID:      cfg.UserID,
		profile:     tgclient.Account{UserID: cfg.UserID, FirstName: cfg.FirstName, Username: cfg.Username},
		script:      newScript(),
		authorized:  cfg.Login == nil,
		nextSavedID: 1,
	}
	if cfg.Login != nil {
		login := *cfg.Login
		c.login = &login
	}
	return c
}

func (c *Client) UserID() int64 {
	return c.userID
}

func (c *Client) PeerOf(channelID int64) (tgclient.InputPeer, error) {
	c.n.mu.Lock()
	defer c.n.mu.Unlock()
	ch, err := c.n.channelLocked(channelID)
	if err != nil {
		return tgclient.InputPeer{}, err
	}
	if _, member := ch.members[c.userID]; !member {
		return tgclient.InputPeer{}, fmt.Errorf("faketg: channel %d: %w", channelID, tgclient.ErrChannelUnavailable)
	}
	return peerFor(channelID, c.userID), nil
}

func (c *Client) Close() {}

func (c *Client) enter(ctx context.Context, m Method, channelID int64) (Fault, error) {
	if err := ctx.Err(); err != nil {
		return Fault{}, err
	}
	c.n.mu.Lock()
	fault := c.script.next(m)
	if fault.Kind == FaultDeleteMessages {
		target := fault.ChannelID
		if target == 0 {
			target = channelID
		}
		if ch, ok := c.n.channels[target]; ok {
			ch.deleteLocked(fault.MessageIDs)
		}
	}
	latency := c.script.latency[m] + fault.Latency
	c.n.mu.Unlock()
	if latency > 0 {
		if err := c.n.delay(ctx, latency); err != nil {
			return fault, err
		}
	}
	return fault, fault.immediateError()
}

func (c *Client) requireAuthLocked() error {
	if !c.authorized {
		return fmt.Errorf("faketg: %w", tgclient.ErrNotAuthorized)
	}
	return nil
}

func checkPeer(peer tgclient.InputPeer) error {
	if peer.ChannelID <= 0 {
		return fmt.Errorf("faketg: %w: channel id", tgclient.ErrChannelUnavailable)
	}
	if peer.AccessHash == 0 {
		return fmt.Errorf("faketg: channel %d: %w", peer.ChannelID, tgclient.ErrZeroAccessHash)
	}
	return nil
}

func (c *Client) peerChannelLocked(peer tgclient.InputPeer, needPost bool) (*channelState, error) {
	if err := c.requireAuthLocked(); err != nil {
		return nil, err
	}
	ch, err := c.n.channelLocked(peer.ChannelID)
	if err != nil {
		return nil, err
	}
	canPost, member := ch.members[c.userID]
	if !member || peer.AccessHash != accessHashFor(peer.ChannelID, c.userID) {
		return nil, fmt.Errorf("faketg: channel %d: %w", peer.ChannelID, tgclient.ErrChannelUnavailable)
	}
	if needPost && !canPost {
		return nil, fmt.Errorf("faketg: channel %d: %w", peer.ChannelID, ErrWriteForbidden)
	}
	return ch, nil
}

func (c *Client) SavedMessages() []SentSaved {
	c.n.mu.Lock()
	defer c.n.mu.Unlock()
	return append([]SentSaved(nil), c.saved...)
}

func (c *Client) ReadCount() int {
	c.n.mu.Lock()
	defer c.n.mu.Unlock()
	return c.readCalls
}

func (c *Client) CDNRedirectCount() int {
	c.n.mu.Lock()
	defer c.n.mu.Unlock()
	return c.cdnRedirects
}

func (c *Client) Authorized() bool {
	c.n.mu.Lock()
	defer c.n.mu.Unlock()
	return c.authorized
}
