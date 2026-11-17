package app

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/media"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/mediaqueue"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/mediaservice"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/nativeplayer"
)

const (
	playerMissingText  = "Install mpv to play videos and music."
	playerGoneFileText = "A file in this queue no longer exists. Refresh the list and try again."
	playerFailedText   = "mpv could not play one of these files. It may use an unsupported format."
	playerOpenText     = "These files could not be opened for playback."
)

type mediaPlayer struct {
	svc   *mediaservice.Service
	queue *mediaqueue.Queue

	openMu sync.Mutex
}

func newMediaPlayer(ctx context.Context, source media.Source) *mediaPlayer {
	sessions := media.NewService(ctx, source)
	svc := mediaservice.New(ctx, sessions, nil)
	go mediaqueue.Sweep(os.TempDir(), time.Now())
	queue := mediaqueue.New(svc, source, mediaqueue.Options{})
	context.AfterFunc(ctx, func() {
		queue.Stop()
		svc.CloseAllNativeMedia()
		_ = sessions.Close()
	})
	return &mediaPlayer{svc: svc, queue: queue}
}

func playOrder(items []adminv1.ObjectSummary, start int) []adminv1.ObjectSummary {
	if len(items) == 0 {
		return nil
	}
	start = min(max(start, 0), len(items)-1)
	ordered := make([]adminv1.ObjectSummary, 0, len(items))
	for i := range items {
		ordered = append(ordered, items[(start+i)%len(items)])
	}
	return ordered
}

func (p *mediaPlayer) Play(ctx context.Context, bucket string, items []adminv1.ObjectSummary, start int, onError func(string)) {
	ordered := playOrder(items, start)
	if len(ordered) == 0 || ctx.Err() != nil {
		return
	}
	report := func(text string) {
		if onError != nil {
			onError(text)
		}
	}
	go func() {
		p.openMu.Lock()
		defer p.openMu.Unlock()
		err := p.queue.Play(ctx, bucket, ordered, func(err error) { report(queueErrorText(err)) })
		if err != nil {
			report(openErrorText(err))
		}
	}()
}

func queueErrorText(err error) string {
	switch {
	case errors.Is(err, mediaqueue.ErrDownload):
		return "Could not download a file for the queue, so it was skipped: " + strings.TrimPrefix(err.Error(), mediaqueue.ErrDownload.Error()+": ")
	case errors.Is(err, mediaqueue.ErrPlayback):
		return playerFailedText
	default:
		return playerOpenText
	}
}

func openErrorText(err error) string {
	switch {
	case errors.Is(err, nativeplayer.ErrUnsupported), strings.Contains(err.Error(), "mpv executable not found"):
		return playerMissingText
	case errors.Is(err, media.ErrFileNotFound):
		return playerGoneFileText
	default:
		return playerOpenText
	}
}
