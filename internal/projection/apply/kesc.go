package apply

import (
	"context"

	"github.com/PRIEYAN/ButterDB/internal/projection"
)

type keyEscrowApplier struct{}

func (keyEscrowApplier) Apply(ctx context.Context, tx projection.Tx, in projection.ApplyInput) (projection.ApplyResult, error) {
	if err := requireRegistryScope(ctx, tx, in); err != nil {
		return projection.ApplyResult{}, err
	}
	return applied(), nil
}
