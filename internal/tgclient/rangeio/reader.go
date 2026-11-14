package rangeio

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"

	"github.com/gotd/td/tg"
	"golang.org/x/sync/singleflight"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/tgclient/scope"
)

const refreshedReferenceCapacity = 256

type Dependencies struct {
	Invoker   scope.Invoker
	Gate      tgclient.GetFileGate
	Refresher scope.FileReferenceRefresher
	Logger    *slog.Logger
}

type Reader struct {
	invoker   scope.Invoker
	gate      tgclient.GetFileGate
	refresher scope.FileReferenceRefresher
	logger    *slog.Logger

	refresh singleflight.Group

	mu        sync.Mutex
	refreshed map[staleReference]tgclient.DocumentRef
	order     []staleReference
}

type staleReference struct {
	documentID int64
	reference  string
}

func New(deps Dependencies) (*Reader, error) {
	switch {
	case deps.Invoker == nil:
		return nil, fmt.Errorf("rangeio: %w: invoker", errs.ErrMissingDependency)
	case deps.Gate == nil:
		return nil, fmt.Errorf("rangeio: %w: gate", errs.ErrMissingDependency)
	case deps.Refresher == nil:
		return nil, fmt.Errorf("rangeio: %w: refresher", errs.ErrMissingDependency)
	case deps.Logger == nil:
		return nil, fmt.Errorf("rangeio: %w: logger", errs.ErrMissingDependency)
	}
	return &Reader{
		invoker:   deps.Invoker,
		gate:      deps.Gate,
		refresher: deps.Refresher,
		logger:    deps.Logger,
		refreshed: make(map[staleReference]tgclient.DocumentRef),
	}, nil
}

func validateRange(offset int64, length int) error {
	switch {
	case offset < 0:
		return fmt.Errorf("rangeio: %w: negative offset", errs.ErrInvalidRange)
	case offset%RangeReadAlignment != 0:
		return fmt.Errorf("rangeio: %w: offset %d is not %d-aligned", errs.ErrInvalidRange, offset, RangeReadAlignment)
	case length > RangeReadMaxBytes:
		return fmt.Errorf("rangeio: %w: length %d exceeds %d", errs.ErrInvalidRange, length, RangeReadMaxBytes)
	case crossesRangeBoundary(offset, length):
		return fmt.Errorf("rangeio: %w: range crosses a %d-byte boundary", errs.ErrInvalidRange, RangeReadMaxBytes)
	}
	return nil
}

func (r *Reader) ReadDocumentRange(ctx context.Context, ref tgclient.DocumentRef, offset int64, dst []byte) (int, error) {
	if len(dst) == 0 {
		return 0, nil
	}
	if err := validateRange(offset, len(dst)); err != nil {
		return 0, err
	}
	release, err := r.gate.AcquireForeground(ctx, 1)
	if err != nil {
		return 0, fmt.Errorf("rangeio: acquire getFile slot: %w", err)
	}
	defer release()

	used := r.currentReference(ref)
	n, err := r.readBlock(ctx, used, offset, dst)
	if !IsFileReferenceError(err) {
		return n, err
	}
	fresh, err := r.refreshReference(ctx, used)
	if err != nil {
		return 0, fmt.Errorf("rangeio: %w: refresh: %w", tgclient.ErrFileReferenceExpired, err)
	}
	n, err = r.readBlock(ctx, fresh, offset, dst)
	if IsFileReferenceError(err) {
		return 0, fmt.Errorf("rangeio: %w: %w", tgclient.ErrFileReferenceExpired, err)
	}
	return n, err
}

func (r *Reader) readBlock(ctx context.Context, ref tgclient.DocumentRef, offset int64, dst []byte) (int, error) {
	limit := roundedTelegramLimit(len(dst))
	result, err := r.getFile(ctx, ref, offset, limit)
	if err != nil {
		return 0, fmt.Errorf("rangeio: get file at %d: %w", offset, normalizeError(err))
	}
	switch file := result.(type) {
	case *tg.UploadFile:
		return copyRequestedRange(dst, file.Bytes)
	case *tg.UploadFileCDNRedirect:
		return r.readCDNRange(ctx, file, offset, limit, dst)
	default:
		return 0, fmt.Errorf("rangeio: unexpected upload.getFile result %T", result)
	}
}

// A pool pointed at the wrong data center cannot follow FILE_MIGRATE; the primary connection can.
func (r *Reader) getFile(ctx context.Context, ref tgclient.DocumentRef, offset int64, limit int) (tg.UploadFileClass, error) {
	var result tg.UploadFileClass
	call := func(ctx context.Context, api *tg.Client) error {
		request := &tg.UploadGetFileRequest{
			Location: &tg.InputDocumentFileLocation{ID: ref.DocumentID, AccessHash: ref.AccessHash, FileReference: ref.FileReference},
			Offset:   offset,
			Limit:    limit,
		}
		request.SetPrecise(true)
		request.SetCDNSupported(true)
		var err error
		result, err = api.UploadGetFile(ctx, request)
		return err
	}
	if ref.DCID == 0 {
		return result, r.invoker.Invoke(ctx, call)
	}
	err := r.invoker.InvokeMedia(ctx, ref.DCID, call)
	if isFileMigrate(err) {
		err = r.invoker.Invoke(ctx, call)
	}
	return result, err
}

func (r *Reader) refreshReference(ctx context.Context, stale tgclient.DocumentRef) (tgclient.DocumentRef, error) {
	if known, ok := r.refreshedFor(stale); ok {
		return known, nil
	}
	key := strconv.FormatInt(stale.Peer.ChannelID, 10) + ":" + strconv.FormatInt(stale.MsgID, 10) + ":" + string(stale.FileReference)
	value, err, _ := r.refresh.Do(key, func() (any, error) {
		documentID, accessHash, reference, dcID, err := r.refresher.RefreshDocument(ctx, stale.Peer.ChannelID, stale.Peer.AccessHash, stale.MsgID)
		if err != nil {
			return nil, err
		}
		fresh := stale
		fresh.DocumentID = documentID
		fresh.AccessHash = accessHash
		fresh.FileReference = append([]byte(nil), reference...)
		fresh.DCID = dcID
		r.rememberRefresh(stale, fresh)
		r.logger.Debug("rangeio: file reference refreshed", "channel_id", stale.Peer.ChannelID, "msg_id", stale.MsgID)
		return fresh, nil
	})
	if err != nil {
		return tgclient.DocumentRef{}, err
	}
	fresh, ok := value.(tgclient.DocumentRef)
	if !ok {
		return tgclient.DocumentRef{}, errors.New("rangeio: refresh returned an unexpected value")
	}
	return fresh, nil
}

func (r *Reader) currentReference(ref tgclient.DocumentRef) tgclient.DocumentRef {
	if known, ok := r.refreshedFor(ref); ok {
		return known
	}
	return ref
}

func (r *Reader) refreshedFor(ref tgclient.DocumentRef) (tgclient.DocumentRef, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	known, ok := r.refreshed[staleReference{documentID: ref.DocumentID, reference: string(ref.FileReference)}]
	return known, ok
}

func (r *Reader) rememberRefresh(stale, fresh tgclient.DocumentRef) {
	key := staleReference{documentID: stale.DocumentID, reference: string(stale.FileReference)}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.refreshed[key]; !exists {
		r.order = append(r.order, key)
	}
	r.refreshed[key] = fresh
	for len(r.order) > refreshedReferenceCapacity {
		delete(r.refreshed, r.order[0])
		r.order = r.order[1:]
	}
}
