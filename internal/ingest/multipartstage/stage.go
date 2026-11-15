package multipartstage

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/crypto/format"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/limits"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/storage/jobrepo"
)

const (
	uploadIDBytes   = 16
	uploadIDLength  = 2 * uploadIDBytes
	uploadDirMode   = 0o700
	partFileMode    = 0o600
	partFilePattern = "part-%05d"
	partTempPattern = "part-*.tmp"
	indexPageSize   = 1000
	maxListPageSize = 1000
	uuidVersionMask = 0x0f
	uuidVersion4    = 0x40
	uuidVariantMask = 0x3f
	uuidVariantRFC  = 0x80
)

type partReader interface {
	Open(name string) (io.ReadCloser, error)
}

type Stage struct {
	deps   Dependencies
	reader partReader
}

func New(deps Dependencies) (*Stage, error) {
	if deps.Index == nil {
		return nil, fmt.Errorf("multipartstage: %w: index", errs.ErrMissingDependency)
	}
	if deps.Putter == nil {
		return nil, fmt.Errorf("multipartstage: %w: putter", errs.ErrMissingDependency)
	}
	if deps.Dir == "" {
		return nil, fmt.Errorf("multipartstage: %w: directory", errs.ErrMissingDependency)
	}
	if deps.Clock == nil {
		return nil, fmt.Errorf("multipartstage: %w: clock", errs.ErrMissingDependency)
	}
	if deps.Random == nil {
		return nil, fmt.Errorf("multipartstage: %w: random", errs.ErrMissingDependency)
	}
	if deps.FS == nil {
		return nil, fmt.Errorf("multipartstage: %w: filesystem", errs.ErrMissingDependency)
	}
	reader, ok := deps.FS.(partReader)
	if !ok {
		return nil, fmt.Errorf("multipartstage: %w: filesystem without Open", errs.ErrMissingDependency)
	}
	return &Stage{deps: deps, reader: reader}, nil
}

func (s *Stage) Create(ctx context.Context, req CreateRequest) (Upload, error) {
	row, err := s.newUploadRow(req)
	if err != nil {
		return Upload{}, err
	}
	if err := s.deps.Index.CreateUpload(ctx, row); err != nil {
		return Upload{}, fmt.Errorf("multipartstage: create upload: %w", err)
	}
	if err := s.deps.FS.MkdirAll(s.uploadDir(row.UploadID), uploadDirMode); err != nil {
		_ = s.deps.Index.Delete(context.WithoutCancel(ctx), row.UploadID)
		return Upload{}, fmt.Errorf("multipartstage: create upload directory: %w", err)
	}
	return uploadFromRow(row), nil
}

func (s *Stage) newUploadRow(req CreateRequest) (jobrepo.MultipartUpload, error) {
	if err := req.Key.Validate(); err != nil {
		return jobrepo.MultipartUpload{}, fmt.Errorf("multipartstage: create upload: %w: %w", errs.ErrInvalidKey, err)
	}
	if req.BucketChannelID <= 0 || req.Bucket == "" || req.ContentType == "" {
		return jobrepo.MultipartUpload{}, fmt.Errorf("multipartstage: create upload: %w: bucket and content type are required", errs.ErrInvalidKey)
	}
	userMeta, err := marshalMetadata(req.UserMeta)
	if err != nil {
		return jobrepo.MultipartUpload{}, err
	}
	systemMeta, err := marshalMetadata(req.SystemMeta)
	if err != nil {
		return jobrepo.MultipartUpload{}, err
	}
	id, err := s.newUploadID()
	if err != nil {
		return jobrepo.MultipartUpload{}, err
	}
	now := s.deps.Clock.Now().UnixMilli()
	return jobrepo.MultipartUpload{
		UploadID:             id,
		BucketChannelID:      req.BucketChannelID,
		BucketName:           string(req.Bucket),
		ObjectKey:            string(req.Key),
		InitiatorAccessKeyID: req.AccessKeyID,
		InitiatedAt:          now,
		LastActivityAt:       now,
		ContentType:          req.ContentType,
		UserMetaJSON:         userMeta,
		SystemMetaJSON:       systemMeta,
		Encrypted:            req.Encrypted,
		ChecksumAlgorithm:    req.ChecksumAlgorithm,
		State:                jobrepo.MultipartOpen,
	}, nil
}

func (s *Stage) newUploadID() (string, error) {
	var raw [uploadIDBytes]byte
	if _, err := io.ReadFull(s.deps.Random, raw[:]); err != nil {
		return "", fmt.Errorf("multipartstage: mint upload id: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func (s *Stage) newJobID() (string, error) {
	var raw [16]byte
	if _, err := io.ReadFull(s.deps.Random, raw[:]); err != nil {
		return "", fmt.Errorf("multipartstage: mint job id: %w", err)
	}
	raw[6] = raw[6]&uuidVersionMask | uuidVersion4
	raw[8] = raw[8]&uuidVariantMask | uuidVariantRFC
	encoded := hex.EncodeToString(raw[:])
	return strings.Join([]string{encoded[0:8], encoded[8:12], encoded[12:16], encoded[16:20], encoded[20:32]}, "-"), nil
}

func marshalMetadata(meta map[string]string) (string, error) {
	if len(meta) == 0 {
		return "{}", nil
	}
	encoded, err := json.Marshal(meta)
	if err != nil {
		return "", fmt.Errorf("multipartstage: encode metadata: %w", err)
	}
	return string(encoded), nil
}

func unmarshalMetadata(raw string) (map[string]string, error) {
	meta := map[string]string{}
	if raw == "" {
		return meta, nil
	}
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		return nil, fmt.Errorf("multipartstage: decode metadata: %w: %w", jobrepo.ErrCorruptRow, err)
	}
	return meta, nil
}

func validateUploadID(id string) error {
	if len(id) != uploadIDLength {
		return fmt.Errorf("multipartstage: upload id: %w", errs.ErrNoSuchUpload)
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return fmt.Errorf("multipartstage: upload id: %w", errs.ErrNoSuchUpload)
		}
	}
	return nil
}

func (s *Stage) uploadDir(id string) string {
	return filepath.Join(s.deps.Dir, id)
}

func (s *Stage) partPath(id string, number int) string {
	return filepath.Join(s.uploadDir(id), fmt.Sprintf(partFilePattern, number))
}

func (s *Stage) removeUploadFiles(id string) error {
	dir := s.uploadDir(id)
	entries, err := s.deps.FS.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("multipartstage: read upload directory: %w", err)
	}
	var failures []error
	for _, entry := range entries {
		if err := s.deps.FS.Remove(filepath.Join(dir, entry.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			failures = append(failures, err)
		}
	}
	if err := s.deps.FS.Remove(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		failures = append(failures, err)
	}
	if err := errors.Join(failures...); err != nil {
		return fmt.Errorf("multipartstage: remove upload files: %w", err)
	}
	return nil
}

func (s *Stage) listAllParts(ctx context.Context, id string) ([]jobrepo.MultipartPart, error) {
	var all []jobrepo.MultipartPart
	marker := 0
	for {
		page, more, err := s.deps.Index.ListParts(ctx, id, marker, indexPageSize)
		if err != nil {
			return nil, fmt.Errorf("multipartstage: list staged parts: %w", err)
		}
		all = append(all, page...)
		if !more || len(page) == 0 {
			return all, nil
		}
		marker = page[len(page)-1].PartNumber
	}
}

func (s *Stage) ListParts(ctx context.Context, uploadID string, marker int, max int) (PartPage, error) {
	if err := validateUploadID(uploadID); err != nil {
		return PartPage{}, err
	}
	rows, truncated, err := s.deps.Index.ListParts(ctx, uploadID, marker, clampPageSize(max))
	if err != nil {
		return PartPage{}, fmt.Errorf("multipartstage: list parts: %w", err)
	}
	page := PartPage{Parts: make([]Part, 0, len(rows)), Truncated: truncated}
	for _, row := range rows {
		page.Parts = append(page.Parts, partFromRow(row))
	}
	if truncated && len(rows) > 0 {
		page.NextMarker = rows[len(rows)-1].PartNumber
	}
	return page, nil
}

func clampPageSize(requested int) int {
	if requested <= 0 || requested > maxListPageSize {
		return maxListPageSize
	}
	return requested
}

func (s *Stage) ListUploads(ctx context.Context, bucketChannelID int64, q UploadQuery) (UploadPage, error) {
	walker := uploadWalker{limit: clampPageSize(q.Max), query: q}
	walker.seedMarkerPrefix()
	cursor := q
	for {
		cursor.Max = indexPageSize
		rows, more, err := s.deps.Index.ListUploads(ctx, bucketChannelID, cursor)
		if err != nil {
			return UploadPage{}, fmt.Errorf("multipartstage: list uploads: %w", err)
		}
		for _, row := range rows {
			if walker.add(row) {
				return walker.page, nil
			}
		}
		if !more || len(rows) == 0 {
			return walker.page, nil
		}
		last := rows[len(rows)-1]
		cursor.KeyMarker, cursor.UploadIDMarker = last.ObjectKey, last.UploadID
	}
}

type uploadWalker struct {
	query UploadQuery
	limit int
	count int
	seen  map[string]struct{}
	page  UploadPage
}

func (w *uploadWalker) seedMarkerPrefix() {
	w.seen = make(map[string]struct{})
	if w.query.Delimiter == "" || w.query.KeyMarker == "" {
		return
	}
	if prefix, ok := w.commonPrefixOf(w.query.KeyMarker); ok && prefix == w.query.KeyMarker {
		w.seen[prefix] = struct{}{}
	}
}

func (w *uploadWalker) commonPrefixOf(key string) (string, bool) {
	if w.query.Delimiter == "" || !strings.HasPrefix(key, w.query.Prefix) {
		return "", false
	}
	rest := key[len(w.query.Prefix):]
	at := strings.Index(rest, w.query.Delimiter)
	if at < 0 {
		return "", false
	}
	return w.query.Prefix + rest[:at+len(w.query.Delimiter)], true
}

func (w *uploadWalker) add(row jobrepo.MultipartUpload) bool {
	if prefix, ok := w.commonPrefixOf(row.ObjectKey); ok {
		return w.addPrefix(prefix)
	}
	if w.count == w.limit {
		w.page.Truncated = true
		return true
	}
	w.page.Uploads = append(w.page.Uploads, uploadFromRow(row))
	w.count++
	w.page.NextKeyMarker, w.page.NextUploadIDMarker = row.ObjectKey, row.UploadID
	return false
}

func (w *uploadWalker) addPrefix(prefix string) bool {
	if _, dup := w.seen[prefix]; dup {
		return false
	}
	if w.count == w.limit {
		w.page.Truncated = true
		return true
	}
	w.seen[prefix] = struct{}{}
	w.page.CommonPrefixes = append(w.page.CommonPrefixes, prefix)
	w.count++
	w.page.NextKeyMarker, w.page.NextUploadIDMarker = prefix, ""
	return false
}

func uploadFromRow(row jobrepo.MultipartUpload) Upload {
	return Upload{
		ID:          row.UploadID,
		Bucket:      bucket.BucketName(row.BucketName),
		Key:         object.Key(row.ObjectKey),
		InitiatedAt: time.UnixMilli(row.InitiatedAt),
		State:       string(row.State),
	}
}

func partFromRow(row jobrepo.MultipartPart) Part {
	return Part{
		Number:   row.PartNumber,
		Size:     row.Size,
		ETag:     row.MD5,
		Checksum: row.ChecksumValue,
		StagedAt: time.UnixMilli(row.StagedAt),
	}
}

func storedSizeOf(encrypted bool, plaintext int64) int64 {
	if encrypted {
		return format.CiphertextSize(plaintext)
	}
	return plaintext
}

func exceedsStoredCeiling(encrypted bool, plaintext int64) bool {
	return storedSizeOf(encrypted, plaintext) > limits.MaxStoredObjectBytes
}
