package send

import (
	"errors"

	"github.com/gotd/td/tgerr"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

var ErrInvalidRandomID = errors.New("send: invalid random id")

var ErrInvalidRequest = errors.New("send: invalid request")

func normalizeError(err error) error {
	if err == nil {
		return nil
	}
	var flood *tgclient.FloodWaitError
	if errors.As(err, &flood) {
		return err
	}
	if wait, ok := tgerr.AsFloodWait(err); ok {
		return &tgclient.FloodWaitError{Duration: wait}
	}
	return err
}

func isFloodWait(err error) bool {
	var flood *tgclient.FloodWaitError
	return errors.As(err, &flood)
}
