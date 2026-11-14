package rangeio

import (
	"errors"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

var ErrNotDocument = errors.New("rangeio: message carries no document")

func IsFileReferenceError(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, tgclient.ErrFileReferenceExpired) ||
		tg.IsFileReferenceEmpty(err) || tg.IsFileReferenceExpired(err) || tg.IsFileReferenceInvalid(err)
}

func isFileMigrate(err error) bool {
	rpcErr, ok := tgerr.As(err)
	return ok && rpcErr.Type == "FILE_MIGRATE"
}

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
