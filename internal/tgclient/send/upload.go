package send

import (
	"context"
	"fmt"
	"io"

	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"

	"github.com/PRIEYAN/ButterDB/internal/tgclient/scope"
)

const (
	UploadThreads  = 8
	uploadPartSize = uploader.MaximumPartSize
)

type retryingPartClient struct {
	invoker scope.Invoker
	slots   UploadSlots
	retry   RetryPolicy
}

func (c *retryingPartClient) UploadSaveFilePart(ctx context.Context, request *tg.UploadSaveFilePartRequest) (bool, error) {
	return c.savePart(ctx, func(ctx context.Context, api *tg.Client) (bool, error) {
		return api.UploadSaveFilePart(ctx, request)
	})
}

func (c *retryingPartClient) UploadSaveBigFilePart(ctx context.Context, request *tg.UploadSaveBigFilePartRequest) (bool, error) {
	return c.savePart(ctx, func(ctx context.Context, api *tg.Client) (bool, error) {
		return api.UploadSaveBigFilePart(ctx, request)
	})
}

// The slot is held for the request only, never across a retry backoff.
func (c *retryingPartClient) savePart(ctx context.Context, action func(context.Context, *tg.Client) (bool, error)) (bool, error) {
	var accepted bool
	err := c.retry.Do(ctx, func() error {
		release, err := c.slots.Acquire(ctx)
		if err != nil {
			return err
		}
		defer release()
		return c.invoker.Invoke(ctx, func(ctx context.Context, api *tg.Client) error {
			var err error
			accepted, err = action(ctx, api)
			return normalizeError(err)
		})
	})
	return accepted, err
}

func (s *Sender) upload(ctx context.Context, name string, body io.ReadSeeker, size int64) (tg.InputFileClass, error) {
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("send: rewind body: %w", err)
	}
	parts := &retryingPartClient{invoker: s.invoker, slots: s.slots, retry: s.uploadRetry}
	u := uploader.NewUploader(parts).WithPartSize(uploadPartSize).WithThreads(UploadThreads)
	file, err := u.Upload(ctx, uploader.NewUpload(name, body, size))
	if err != nil {
		return nil, fmt.Errorf("send: upload: %w", err)
	}
	return file, nil
}
