package core

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/identity"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/platform/config"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/authrpc"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/conn"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/rangeio"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/rpc"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/scope"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/send"
)

var (
	_ tgclient.Sender      = (*send.Sender)(nil)
	_ tgclient.RangeReader = (*rangeio.Reader)(nil)
	_ tgclient.ChannelRPC  = (*rpc.Client)(nil)
	_ tgclient.AuthRPC     = (*authrpc.Client)(nil)
	_ scope.Invoker        = (*conn.Connection)(nil)
	_ scope.SessionStore   = (*identity.SessionStore)(nil)
)

type TransportDependencies struct {
	Credentials identity.Credentials
	Sessions    scope.SessionStore
	Flow        *FlowControl
	Telegram    config.TelegramConfig
	Clock       clock.Clock
	Random      io.Reader
	Logger      *slog.Logger
}

type Transport struct {
	Connection *conn.Connection
	Activity   *conn.Activity
	Sender     *send.Sender
	Reader     *rangeio.Reader
	Stream     *rangeio.Stream
	Channels   *rpc.Client
	Auth       *authrpc.Client
	Flow       *FlowControl
}

func NewTransport(deps TransportDependencies) (*Transport, error) {
	switch {
	case deps.Sessions == nil:
		return nil, fmt.Errorf("core: transport: %w: sessions", errs.ErrMissingDependency)
	case deps.Flow == nil:
		return nil, fmt.Errorf("core: transport: %w: flow control", errs.ErrMissingDependency)
	case deps.Clock == nil:
		return nil, fmt.Errorf("core: transport: %w: clock", errs.ErrMissingDependency)
	case deps.Random == nil:
		return nil, fmt.Errorf("core: transport: %w: random", errs.ErrMissingDependency)
	case deps.Logger == nil:
		return nil, fmt.Errorf("core: transport: %w: logger", errs.ErrMissingDependency)
	}
	activity := conn.NewActivity(0)
	connection, err := conn.New(conn.Dependencies{
		APIID:       deps.Credentials.APIID,
		APIHash:     deps.Credentials.APIHash,
		Sessions:    deps.Sessions,
		DeviceModel: deps.Telegram.DeviceModel,
		TestDC:      deps.Telegram.TestDC,
		Clock:       deps.Clock,
		Logger:      deps.Logger,
		Activity:    activity,
	})
	if err != nil {
		return nil, fmt.Errorf("core: transport: connection: %w", err)
	}
	transport, err := assembleTransport(deps, connection, activity)
	if err != nil {
		_ = connection.Close(context.Background())
		return nil, err
	}
	return transport, nil
}

func assembleTransport(deps TransportDependencies, connection *conn.Connection, activity *conn.Activity) (*Transport, error) {
	flowControl := deps.Flow
	sender, err := send.New(send.Dependencies{
		Invoker:      connection,
		Writes:       flowControl.Writes,
		Slots:        flowControl.UploadSlots,
		UploadRetry:  flowControl.UploadRetry,
		ControlRetry: flowControl.ControlRetry,
		Logger:       deps.Logger,
	})
	if err != nil {
		return nil, fmt.Errorf("core: transport: sender: %w", err)
	}
	reader, err := rangeio.New(rangeio.Dependencies{
		Invoker:   connection,
		Gate:      flowControl.GetFile,
		Refresher: connection,
		Logger:    deps.Logger,
	})
	if err != nil {
		return nil, fmt.Errorf("core: transport: range reader: %w", err)
	}
	stream, err := rangeio.NewStream(rangeio.StreamDependencies{Blocks: reader, Resolver: reader, Retry: flowControl.BlockRetry})
	if err != nil {
		return nil, fmt.Errorf("core: transport: stream: %w", err)
	}
	channels, err := rpc.New(rpc.Dependencies{Invoker: connection, Gate: flowControl.Writes, Random: deps.Random})
	if err != nil {
		return nil, fmt.Errorf("core: transport: channel rpc: %w", err)
	}
	auth, err := authrpc.New(authrpc.Dependencies{
		Invoker: connection,
		Random:  deps.Random,
		APIID:   deps.Credentials.APIID,
		APIHash: deps.Credentials.APIHash,
	})
	if err != nil {
		return nil, fmt.Errorf("core: transport: auth rpc: %w", err)
	}
	return &Transport{
		Connection: connection,
		Activity:   activity,
		Sender:     sender,
		Reader:     reader,
		Stream:     stream,
		Channels:   channels,
		Auth:       auth,
		Flow:       flowControl,
	}, nil
}

func (t *Transport) Start(ctx context.Context) error {
	return t.Connection.Start(ctx)
}

func (t *Transport) Close(ctx context.Context) error {
	return t.Connection.Close(ctx)
}
