package difference

import (
	"context"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/projection"
	"github.com/PRIEYAN/ButterDB/internal/projection/rebuild"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

type DifferenceReader interface {
	GetChannelDifference(ctx context.Context, peer tgclient.InputPeer, pts int, limit int) (tgclient.ChannelDifference, error)
}

type Sleeper interface {
	Sleep(ctx context.Context, wait time.Duration) error
}

type OpProjector interface {
	ProjectFromOpTx(ctx context.Context, tx projection.Tx, obs projection.Observed) (projection.Outcome, error)
}

type KeyRepairer interface {
	NeedsKeyRepair(ctx context.Context, tx projection.Tx, channelID int64, key string, msgID int64) (bool, error)
	RepairKeyTx(ctx context.Context, tx projection.Tx, channelID int64, key string) (rebuild.Result, error)
}
