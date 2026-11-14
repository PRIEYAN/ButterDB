package rangeio

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"

	"github.com/gotd/td/tg"
)

var errCDNReuploadRepeated = errors.New("rangeio: cdn token still requires reupload")

func (r *Reader) readCDNRange(ctx context.Context, redirect *tg.UploadFileCDNRedirect, offset int64, limit int, dst []byte) (int, error) {
	plain, err := r.readCDNPlain(ctx, redirect, offset, limit)
	if err != nil {
		return 0, err
	}
	if err := r.verifyCDNRange(ctx, redirect, offset, plain); err != nil {
		return 0, err
	}
	return copyRequestedRange(dst, plain)
}

// Telegram asks for a re-upload at most once per read; a second request means the token is unusable.
func (r *Reader) readCDNPlain(ctx context.Context, redirect *tg.UploadFileCDNRedirect, offset int64, limit int) ([]byte, error) {
	for attempt := 0; attempt < 2; attempt++ {
		var result tg.UploadCDNFileClass
		err := r.invoker.InvokeMedia(ctx, redirect.DCID, func(ctx context.Context, api *tg.Client) error {
			var err error
			result, err = api.UploadGetCDNFile(ctx, &tg.UploadGetCDNFileRequest{
				FileToken: redirect.FileToken,
				Offset:    offset,
				Limit:     limit,
			})
			return err
		})
		if err != nil {
			return nil, fmt.Errorf("rangeio: cdn get file: %w", normalizeError(err))
		}
		switch file := result.(type) {
		case *tg.UploadCDNFile:
			return decryptCDNBytes(redirect, file.Bytes, offset)
		case *tg.UploadCDNFileReuploadNeeded:
			if attempt > 0 {
				return nil, errCDNReuploadRepeated
			}
			if err := r.reuploadCDNFile(ctx, redirect, file); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("rangeio: unexpected upload.getCdnFile result %T", result)
		}
	}
	return nil, errCDNReuploadRepeated
}

func (r *Reader) reuploadCDNFile(ctx context.Context, redirect *tg.UploadFileCDNRedirect, needed *tg.UploadCDNFileReuploadNeeded) error {
	err := r.invoker.Invoke(ctx, func(ctx context.Context, api *tg.Client) error {
		_, err := api.UploadReuploadCDNFile(ctx, &tg.UploadReuploadCDNFileRequest{
			FileToken:    redirect.FileToken,
			RequestToken: needed.RequestToken,
		})
		return err
	})
	if err != nil {
		return fmt.Errorf("rangeio: reupload cdn file: %w", normalizeError(err))
	}
	return nil
}

func (r *Reader) verifyCDNRange(ctx context.Context, redirect *tg.UploadFileCDNRedirect, offset int64, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	var hashes []tg.FileHash
	err := r.invoker.InvokeMedia(ctx, redirect.DCID, func(ctx context.Context, api *tg.Client) error {
		var err error
		hashes, err = api.UploadGetCDNFileHashes(ctx, &tg.UploadGetCDNFileHashesRequest{
			FileToken: redirect.FileToken,
			Offset:    offset,
		})
		return err
	})
	if err != nil {
		return fmt.Errorf("rangeio: cdn hashes: %w", normalizeError(err))
	}
	fetch := func(h tg.FileHash) ([]byte, error) {
		return r.readCDNPlain(ctx, redirect, h.Offset, h.Limit)
	}
	if err := verifyFileHashes(offset, data, hashes, fetch); err != nil {
		return fmt.Errorf("rangeio: cdn integrity: %w", err)
	}
	return nil
}

func verifyFileHashes(offset int64, data []byte, hashes []tg.FileHash, fetch func(tg.FileHash) ([]byte, error)) error {
	if len(data) == 0 {
		return nil
	}
	sorted := append([]tg.FileHash(nil), hashes...)
	slices.SortFunc(sorted, func(a, b tg.FileHash) int {
		switch {
		case a.Offset < b.Offset:
			return -1
		case a.Offset > b.Offset:
			return 1
		default:
			return 0
		}
	})

	end := offset + int64(len(data))
	pos := offset
	for _, h := range sorted {
		if h.Limit <= 0 {
			continue
		}
		hashStart := h.Offset
		if hashStart >= end {
			break
		}
		chunk, err := hashChunkData(offset, data, h, fetch)
		if err != nil {
			return err
		}
		hashEnd := h.Offset + int64(len(chunk))
		if hashEnd <= pos {
			continue
		}
		if hashStart > pos {
			return fmt.Errorf("hash coverage gap at %d", pos)
		}
		sum := sha256.Sum256(chunk)
		if !slices.Equal(sum[:], h.Hash) {
			return fmt.Errorf("hash mismatch at %d", hashStart)
		}
		pos = min(hashEnd, end)
		if pos == end {
			return nil
		}
	}
	return fmt.Errorf("hash coverage ended at %d, want %d", pos, end)
}

func hashChunkData(offset int64, data []byte, h tg.FileHash, fetch func(tg.FileHash) ([]byte, error)) ([]byte, error) {
	relStart := h.Offset - offset
	relEnd := relStart + int64(h.Limit)
	if relStart >= 0 && relEnd <= int64(len(data)) {
		return data[relStart:relEnd], nil
	}
	if fetch == nil {
		return nil, fmt.Errorf("hash range %d-%d is not fully available", h.Offset, h.Offset+int64(h.Limit))
	}
	chunk, err := fetch(h)
	if err != nil {
		return nil, fmt.Errorf("fetch hash range %d-%d: %w", h.Offset, h.Offset+int64(h.Limit), err)
	}
	if len(chunk) == 0 {
		return nil, fmt.Errorf("empty hash range %d-%d", h.Offset, h.Offset+int64(h.Limit))
	}
	return chunk, nil
}

func decryptCDNBytes(redirect *tg.UploadFileCDNRedirect, src []byte, offset int64) ([]byte, error) {
	block, err := aes.NewCipher(redirect.EncryptionKey)
	if err != nil {
		return nil, fmt.Errorf("rangeio: cdn cipher: %w", err)
	}
	if block.BlockSize() != len(redirect.EncryptionIv) {
		return nil, errors.New("rangeio: invalid cdn iv length")
	}
	iv := append([]byte(nil), redirect.EncryptionIv...)
	binary.BigEndian.PutUint32(iv[len(iv)-4:], uint32(offset/16))
	dst := make([]byte, len(src))
	cipher.NewCTR(block, iv).XORKeyStream(dst, src)
	return dst, nil
}
