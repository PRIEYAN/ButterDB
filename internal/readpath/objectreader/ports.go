package objectreader

import (
	"context"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

type PeerResolver interface {
	ResolvePeer(ctx context.Context, channelID int64) (tgclient.InputPeer, error)
}

type DocumentResolver interface {
	ResolveDocument(ctx context.Context, peer tgclient.InputPeer, msgID int64) (tgclient.DocumentRef, error)
}

type DEK interface {
	Bytes() []byte
	Clear()
}

type KeyProvider interface {
	DEK(ctx context.Context, bucketChannelID int64, keyID string) (DEK, error)
}
