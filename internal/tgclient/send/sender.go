package send

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/gotd/td/tg"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/scope"
)

type RetryPolicy interface {
	Do(ctx context.Context, action func() error) error
}

type UploadSlots interface {
	Acquire(ctx context.Context) (release func(), err error)
}

type Dependencies struct {
	Invoker      scope.Invoker
	Writes       tgclient.WriteGate
	Slots        UploadSlots
	UploadRetry  RetryPolicy
	ControlRetry RetryPolicy
	Logger       *slog.Logger
}

type Sender struct {
	invoker      scope.Invoker
	writes       tgclient.WriteGate
	slots        UploadSlots
	uploadRetry  RetryPolicy
	controlRetry RetryPolicy
	logger       *slog.Logger
}

type receipt struct {
	msgID   int64
	updates tg.UpdatesClass
}

func New(deps Dependencies) (*Sender, error) {
	switch {
	case deps.Invoker == nil:
		return nil, fmt.Errorf("send: %w: invoker", errs.ErrMissingDependency)
	case deps.Writes == nil:
		return nil, fmt.Errorf("send: %w: writes", errs.ErrMissingDependency)
	case deps.Slots == nil:
		return nil, fmt.Errorf("send: %w: slots", errs.ErrMissingDependency)
	case deps.UploadRetry == nil:
		return nil, fmt.Errorf("send: %w: upload retry", errs.ErrMissingDependency)
	case deps.ControlRetry == nil:
		return nil, fmt.Errorf("send: %w: control retry", errs.ErrMissingDependency)
	case deps.Logger == nil:
		return nil, fmt.Errorf("send: %w: logger", errs.ErrMissingDependency)
	}
	return &Sender{
		invoker:      deps.Invoker,
		writes:       deps.Writes,
		slots:        deps.Slots,
		uploadRetry:  deps.UploadRetry,
		controlRetry: deps.ControlRetry,
		logger:       deps.Logger,
	}, nil
}

func (s *Sender) SendControlIdempotent(ctx context.Context, req tgclient.SendControlRequest) (int64, error) {
	if err := validateTarget(req.Peer, req.RandomID); err != nil {
		return 0, err
	}
	if req.Text == "" {
		return 0, fmt.Errorf("%w: empty text", ErrInvalidRequest)
	}
	sent, err := s.publish(ctx, s.controlRetry, tgclient.WriteControl, req.RandomID, "send control",
		func(ctx context.Context, api *tg.Client) (tg.UpdatesClass, error) {
			return api.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{
				Peer:      toPeer(req.Peer),
				Message:   req.Text,
				RandomID:  req.RandomID,
				Silent:    true,
				NoWebpage: true,
			})
		})
	return sent.msgID, err
}

func (s *Sender) SendFileIdempotent(ctx context.Context, req tgclient.SendFileRequest) (tgclient.SendFileResult, error) {
	if err := validateTarget(req.Peer, req.RandomID); err != nil {
		return tgclient.SendFileResult{}, err
	}
	if req.Body == nil || req.FileName == "" || req.Size <= 0 {
		return tgclient.SendFileResult{}, fmt.Errorf("%w: body, file name and positive size are required", ErrInvalidRequest)
	}
	file, err := s.upload(ctx, req.FileName, req.Body, req.Size)
	if err != nil {
		return tgclient.SendFileResult{}, err
	}
	media := &tg.MessagesSendMediaRequest{
		Peer:     toPeer(req.Peer),
		Silent:   true,
		RandomID: req.RandomID,
		Message:  req.Caption,
		Media: &tg.InputMediaUploadedDocument{
			File:       file,
			MimeType:   "application/octet-stream",
			ForceFile:  true,
			Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: req.FileName}},
		},
	}
	sent, err := s.publish(ctx, s.uploadRetry, tgclient.WriteDocument, req.RandomID, "send file",
		func(ctx context.Context, api *tg.Client) (tg.UpdatesClass, error) {
			return api.MessagesSendMedia(ctx, media)
		})
	if err != nil {
		return tgclient.SendFileResult{}, err
	}
	return tgclient.SendFileResult{
		MsgID:    sent.msgID,
		Document: documentRefFromUpdates(req.Peer, sent.msgID, sent.updates),
	}, nil
}

// The request and random id are retained across attempts; a lost receipt stays an unknown outcome even if a later attempt fails differently.
func (s *Sender) publish(
	ctx context.Context,
	retry RetryPolicy,
	class tgclient.WriteClass,
	randomID int64,
	operation string,
	call func(context.Context, *tg.Client) (tg.UpdatesClass, error),
) (receipt, error) {
	var sent receipt
	var firstUnknown error
	err := retry.Do(ctx, func() error {
		return s.writes.Do(ctx, class, func() error {
			var updates tg.UpdatesClass
			callErr := s.invoker.Invoke(ctx, func(ctx context.Context, api *tg.Client) error {
				var err error
				updates, err = call(ctx, api)
				return err
			})
			var err error
			sent, err = s.classify(updates, callErr, randomID, operation)
			if err != nil && !isFloodWait(err) && firstUnknown == nil {
				firstUnknown = err
			}
			return err
		})
	})
	if err != nil && firstUnknown != nil && !errors.Is(err, firstUnknown) {
		err = errors.Join(err, firstUnknown)
	}
	return sent, err
}

func (s *Sender) classify(updates tg.UpdatesClass, callErr error, randomID int64, operation string) (receipt, error) {
	if msgID := extractMsgID(updates, randomID); msgID > 0 {
		if callErr != nil {
			s.logger.Warn("send: telegram returned a message receipt together with an error", "operation", operation, "msg_id", msgID)
		}
		return receipt{msgID: msgID, updates: updates}, nil
	}
	if callErr == nil {
		_, err := requiredSendMsgID(updates, randomID, operation)
		return receipt{}, err
	}
	callErr = normalizeError(callErr)
	if isFloodWait(callErr) {
		return receipt{}, callErr
	}
	return receipt{}, fmt.Errorf("send: %w: %s: %w", tgclient.ErrSendOutcomeUnknown, operation, callErr)
}

func validateTarget(peer tgclient.InputPeer, randomID int64) error {
	if randomID == 0 {
		return ErrInvalidRandomID
	}
	if peer.ChannelID == 0 || peer.AccessHash == 0 {
		return fmt.Errorf("send: %w", tgclient.ErrZeroAccessHash)
	}
	return nil
}

func toPeer(peer tgclient.InputPeer) *tg.InputPeerChannel {
	return &tg.InputPeerChannel{ChannelID: peer.ChannelID, AccessHash: peer.AccessHash}
}
