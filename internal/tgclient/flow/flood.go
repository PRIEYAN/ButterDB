package flow

import (
	"errors"
	"time"

	"github.com/gotd/td/tgerr"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

const floodWaitWithoutDuration = 2 * time.Second

func FloodWaitDuration(err error) (time.Duration, bool) {
	if err == nil {
		return 0, false
	}
	var typed *tgclient.FloodWaitError
	if errors.As(err, &typed) {
		return typed.Duration, true
	}
	if wait, ok := tgerr.AsFloodWait(err); ok {
		return wait, true
	}
	if errors.Is(err, tgclient.ErrFloodWait) {
		return floodWaitWithoutDuration, true
	}
	return 0, false
}

func ConvertFloodWait(err error) error {
	if err == nil {
		return nil
	}
	var typed *tgclient.FloodWaitError
	if errors.As(err, &typed) {
		return err
	}
	if wait, ok := tgerr.AsFloodWait(err); ok {
		return &tgclient.FloodWaitError{Duration: wait}
	}
	return err
}
