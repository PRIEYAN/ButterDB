package buckets

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/projection"
	"github.com/PRIEYAN/ButterDB/internal/syncer"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

const maxUnknownOutcomeSends = 3

type publication struct {
	channel syncer.ChannelState
	op      wire.Op
	seed    string
	purpose string
	resumed bool
}

type opPublisher struct {
	channels  ChannelRecords
	resolver  PeerResolver
	messages  MessageReader
	sender    ControlSender
	projector OpProjector
	writer    projection.Writer
	randomID  StableRandomIDFunc
	clock     clock.Clock
	logger    *slog.Logger
	actor     int64
}

func newOpPublisher(deps PublishDependencies) (*opPublisher, error) {
	switch {
	case deps.Channels == nil:
		return nil, fmt.Errorf("buckets: %w: channels", errs.ErrMissingDependency)
	case deps.Resolver == nil:
		return nil, fmt.Errorf("buckets: %w: resolver", errs.ErrMissingDependency)
	case deps.Messages == nil:
		return nil, fmt.Errorf("buckets: %w: messages", errs.ErrMissingDependency)
	case deps.Sender == nil:
		return nil, fmt.Errorf("buckets: %w: sender", errs.ErrMissingDependency)
	case deps.Projector == nil:
		return nil, fmt.Errorf("buckets: %w: projector", errs.ErrMissingDependency)
	case deps.Writer == nil:
		return nil, fmt.Errorf("buckets: %w: writer", errs.ErrMissingDependency)
	case deps.StableRandomID == nil:
		return nil, fmt.Errorf("buckets: %w: stable random id", errs.ErrMissingDependency)
	case deps.Clock == nil:
		return nil, fmt.Errorf("buckets: %w: clock", errs.ErrMissingDependency)
	case deps.Logger == nil:
		return nil, fmt.Errorf("buckets: %w: logger", errs.ErrMissingDependency)
	case deps.ActorUserID <= 0:
		return nil, fmt.Errorf("buckets: %w: actor user id", errs.ErrMissingDependency)
	}
	return &opPublisher{
		channels:  deps.Channels,
		resolver:  deps.Resolver,
		messages:  deps.Messages,
		sender:    deps.Sender,
		projector: deps.Projector,
		writer:    deps.Writer,
		randomID:  deps.StableRandomID,
		clock:     deps.Clock,
		logger:    deps.Logger,
		actor:     deps.ActorUserID,
	}, nil
}

func (p *opPublisher) publish(ctx context.Context, pub publication) (int64, error) {
	text, err := wire.Format(pub.op)
	if err != nil {
		return 0, fmt.Errorf("buckets: format %s op: %w", pub.op.Type, err)
	}
	randomID, err := p.randomID(pub.seed, pub.purpose)
	if err != nil {
		return 0, fmt.Errorf("buckets: random id for %s op: %w", pub.op.Type, err)
	}
	msgID, err := p.send(ctx, pub.channel, text, randomID)
	if err != nil {
		return 0, err
	}
	observedText, date := text, p.clock.Now().Unix()
	if pub.resumed {
		observedText, date = p.sentForm(ctx, pub.channel, msgID, text, date)
	}
	outcome, err := p.projector.ProjectFromOp(ctx, p.writer, projection.Observed{
		ChannelID:   pub.channel.ChannelID,
		MsgID:       msgID,
		MsgDate:     date,
		ActorUserID: p.actor,
		RawText:     observedText,
	})
	if err != nil {
		return 0, fmt.Errorf("buckets: project %s op: %w", pub.op.Type, err)
	}
	if err := outcomeError(outcome); err != nil {
		return 0, fmt.Errorf("buckets: project %s op: %w", pub.op.Type, err)
	}
	return msgID, nil
}

func (p *opPublisher) send(ctx context.Context, channel syncer.ChannelState, text string, randomID int64) (int64, error) {
	peer := tgclient.InputPeer{ChannelID: channel.ChannelID, AccessHash: channel.AccessHash}
	refreshed := false
	unknownOutcomes := 0
	for {
		msgID, err := p.sender.SendControlIdempotent(ctx, tgclient.SendControlRequest{Peer: peer, Text: text, RandomID: randomID})
		switch {
		case err == nil && msgID > 0:
			return msgID, nil
		case err == nil:
			return 0, fmt.Errorf("buckets: send control to channel %d: no message id returned", channel.ChannelID)
		case msgID > 0:
			p.logger.Warn("buckets: control send reported an error after the message was accepted", "channel_id", channel.ChannelID, "msg_id", msgID)
			return msgID, nil
		case errors.Is(err, tgclient.ErrSendOutcomeUnknown):
			unknownOutcomes++
			if unknownOutcomes >= maxUnknownOutcomeSends {
				return 0, fmt.Errorf("buckets: send control to channel %d: %w", channel.ChannelID, err)
			}
		case errors.Is(err, tgclient.ErrChannelUnavailable) && !refreshed:
			refreshed = true
			if peer, err = p.refreshPeer(ctx, channel.ChannelID); err != nil {
				return 0, err
			}
		default:
			return 0, fmt.Errorf("buckets: send control to channel %d: %w", channel.ChannelID, err)
		}
	}
}

func (p *opPublisher) refreshPeer(ctx context.Context, channelID int64) (tgclient.InputPeer, error) {
	peer, err := p.resolver.ResolveChannel(ctx, channelID)
	if err != nil {
		return tgclient.InputPeer{}, fmt.Errorf("buckets: resolve channel %d: %w", channelID, err)
	}
	if peer.ChannelID != channelID {
		return tgclient.InputPeer{}, fmt.Errorf("buckets: resolve channel %d: %w", channelID, tgclient.ErrChannelUnavailable)
	}
	if peer.AccessHash == 0 {
		return tgclient.InputPeer{}, fmt.Errorf("buckets: resolve channel %d: %w", channelID, tgclient.ErrZeroAccessHash)
	}
	if err := p.channels.UpdateAccessHash(ctx, channelID, peer.AccessHash); err != nil {
		return tgclient.InputPeer{}, fmt.Errorf("buckets: save access hash for channel %d: %w", channelID, err)
	}
	return peer, nil
}

// A resumed send is deduplicated by Telegram, so the stored message may carry an earlier attempt's text.
func (p *opPublisher) sentForm(ctx context.Context, channel syncer.ChannelState, msgID int64, text string, date int64) (string, int64) {
	peer := tgclient.InputPeer{ChannelID: channel.ChannelID, AccessHash: channel.AccessHash}
	found, err := p.messages.GetMessages(ctx, peer, []int64{msgID})
	if err != nil {
		p.logger.Warn("buckets: read back resumed control message failed", "channel_id", channel.ChannelID, "msg_id", msgID)
		return text, date
	}
	for _, msg := range found {
		if msg.MsgID == msgID && msg.Text != "" {
			return msg.Text, msg.Date
		}
	}
	return text, date
}

func outcomeError(outcome projection.Outcome) error {
	switch outcome.Kind {
	case projection.OutcomeApplied, projection.OutcomeAlreadySeen, projection.OutcomeSuperseded:
		return nil
	case projection.OutcomeTampered:
		return ErrOpTampered
	case projection.OutcomeRejected:
		return rejectError(outcome.RejectCode)
	}
	return fmt.Errorf("%w: %s", ErrOpNotProjected, outcome.Kind)
}

func rejectError(code projection.RejectCode) error {
	switch code {
	case projection.RejectBucketExists:
		return errs.ErrBucketExists
	case projection.RejectBucketNameInvalid:
		return errs.ErrInvalidBucketName
	case projection.RejectBucketNotFound:
		return errs.ErrNoSuchBucket
	}
	return fmt.Errorf("%w: %s", ErrOpRejected, code)
}
