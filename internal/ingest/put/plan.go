package put

import (
	"errors"
	"fmt"
	"strings"

	"github.com/PRIEYAN/ButterDB/internal/crypto/format"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/limits"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/storage/jobrepo"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

const (
	scopeKeyPrefix          = "key:"
	scopeDevicePrefix       = "device:"
	writerKeyPrefix         = "key."
	writerDevicePrefix      = "dev."
	maxIdempotencyKeyLength = 128
)

type plan struct {
	kind              jobrepo.UploadKind
	origin            jobrepo.Origin
	scope             string
	writer            string
	key               object.Key
	contentType       string
	userMeta          map[string]string
	systemMeta        map[string]string
	ifMatch           string
	ifNoneMatch       bool
	idempotencyKey    string
	multipartUploadID string
	etag              string
	encryption        string
	plaintextSize     int64
}

type resolved struct {
	plan
	info       bucket.Info
	encrypted  bool
	storedSize int64
}

func callerIdentity(accessKeyID string) (scope, writer string, origin jobrepo.Origin, err error) {
	if accessKeyID == "" {
		return "", "", "", fmt.Errorf("put: caller identity is required: %w", errs.ErrAccessDenied)
	}
	if id, ok := strings.CutPrefix(accessKeyID, scopeDevicePrefix); ok {
		if id == "" {
			return "", "", "", fmt.Errorf("put: caller identity is required: %w", errs.ErrAccessDenied)
		}
		return accessKeyID, writerDevicePrefix + id, jobrepo.OriginAdmin, nil
	}
	return scopeKeyPrefix + accessKeyID, writerKeyPrefix + accessKeyID, jobrepo.OriginS3, nil
}

func validIdempotencyKey(key string) bool {
	if len(key) > maxIdempotencyKeyLength {
		return false
	}
	for i := 0; i < len(key); i++ {
		if key[i] < 0x20 || key[i] > 0x7e {
			return false
		}
	}
	return true
}

func planRequest(req object.PutRequest) (plan, error) {
	scope, writer, origin, err := callerIdentity(req.AccessKeyID)
	if err != nil {
		return plan{}, err
	}
	if err := req.Key.Validate(); err != nil {
		return plan{}, fmt.Errorf("put: %w: %w", errs.ErrInvalidKey, err)
	}
	if req.ContentLength < 0 {
		return plan{}, fmt.Errorf("put: %w: negative content length", ErrInvalidRequest)
	}
	if !validIdempotencyKey(req.IdempotencyKey) {
		return plan{}, fmt.Errorf("put: %w: idempotency key", ErrInvalidRequest)
	}
	contentType := req.ContentType
	if contentType == "" {
		contentType = wire.DefaultContentType
	}
	if len(contentType) > limits.MaxContentTypeBytes {
		return plan{}, fmt.Errorf("put: %w: content type is too long", ErrInvalidRequest)
	}
	if err := object.ValidateUserMeta(req.UserMeta); err != nil {
		if errors.Is(err, object.ErrMetadataTooLarge) {
			return plan{}, fmt.Errorf("put: %w: %w", errs.ErrMetadataTooLarge, err)
		}
		return plan{}, fmt.Errorf("put: %w: %w", ErrInvalidRequest, err)
	}
	if err := wire.FitsPut(string(req.Key), contentType, req.UserMeta, req.SystemMeta, writer); err != nil {
		if errors.Is(err, wire.ErrWireTooLong) {
			return plan{}, fmt.Errorf("put: %w: %w", errs.ErrMetadataTooLarge, err)
		}
		return plan{}, fmt.Errorf("put: %w: %w", ErrInvalidRequest, err)
	}
	return plan{
		kind:           jobrepo.KindPut,
		origin:         origin,
		scope:          scope,
		writer:         writer,
		key:            req.Key,
		contentType:    contentType,
		userMeta:       req.UserMeta,
		systemMeta:     req.SystemMeta,
		ifMatch:        normalizeETag(req.IfMatch),
		ifNoneMatch:    req.IfNoneMatchAny,
		idempotencyKey: req.IdempotencyKey,
		encryption:     req.Encryption,
		plaintextSize:  req.ContentLength,
	}, nil
}

func normalizeETag(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "W/")
	return strings.Trim(value, `"`)
}

func resolveEncryption(requested string, bucketDefault string) (bool, error) {
	switch requested {
	case "":
		return bucketDefault == bucket.EncryptionTDE1, nil
	case bucket.EncryptionNone:
		return false, nil
	case bucket.EncryptionTDE1:
		return true, nil
	}
	return false, fmt.Errorf("put: %w: encryption %q", ErrInvalidRequest, requested)
}

func storedSizeFor(encrypted bool, plaintextSize int64) (int64, error) {
	if !encrypted {
		if plaintextSize > limits.MaxStoredObjectBytes {
			return 0, fmt.Errorf("put: object of %d bytes: %w", plaintextSize, errs.ErrObjectTooLarge)
		}
		return plaintextSize, nil
	}
	if err := format.ValidatePlaintextSize(plaintextSize); err != nil {
		return 0, fmt.Errorf("put: encrypted object of %d bytes: %w: %w", plaintextSize, errs.ErrObjectTooLarge, err)
	}
	if plaintextSize > limits.MaxEncryptedPlaintextBytes {
		return 0, fmt.Errorf("put: encrypted object of %d bytes: %w", plaintextSize, errs.ErrObjectTooLarge)
	}
	stored := format.CiphertextSize(plaintextSize)
	if stored > limits.MaxStoredObjectBytes {
		return 0, fmt.Errorf("put: encrypted object stores %d bytes: %w", stored, errs.ErrObjectTooLarge)
	}
	return stored, nil
}

func (p plan) resolve(info bucket.Info) (resolved, error) {
	encrypted, err := resolveEncryption(p.encryption, info.DefaultEncryption)
	if err != nil {
		return resolved{}, err
	}
	stored, err := storedSizeFor(encrypted, p.plaintextSize)
	if err != nil {
		return resolved{}, err
	}
	return resolved{plan: p, info: info, encrypted: encrypted, storedSize: stored}, nil
}

func maxPlaintextFor(encrypted bool) int64 {
	if encrypted {
		return limits.MaxEncryptedPlaintextBytes
	}
	return limits.MaxStoredObjectBytes
}
