package scope

import (
	"context"

	"github.com/gotd/td/tg"
)

type Invoker interface {
	Invoke(ctx context.Context, fn func(ctx context.Context, api *tg.Client) error) error
	InvokeMedia(ctx context.Context, dcID int, fn func(ctx context.Context, api *tg.Client) error) error
}

type SessionStore interface {
	LoadSession(ctx context.Context) ([]byte, error)
	StoreSession(ctx context.Context, data []byte) error
}

type FileReferenceRefresher interface {
	RefreshDocument(ctx context.Context, peerChannelID, peerAccessHash, msgID int64) (documentID, accessHash int64, fileReference []byte, dcID int, err error)
}
