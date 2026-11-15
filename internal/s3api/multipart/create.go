package multipart

import (
	"errors"
	"net/http"

	"github.com/PRIEYAN/ButterDB/internal/access/sigv4"
	"github.com/PRIEYAN/ButterDB/internal/domain/access"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/ingest/multipartstage"
	"github.com/PRIEYAN/ButterDB/internal/s3api/router"
)

const writerKeyPrefix = "key."

func (h *Handlers) CreateMultipartUpload(w http.ResponseWriter, r *http.Request, rt router.Route, a sigv4.Auth) {
	info, err := h.authorize(r, rt, a, access.ActionWrite)
	if err != nil {
		h.fail(w, r, rt, err)
		return
	}
	key, err := parseObjectKey(rt.Target.Key)
	if err != nil {
		h.fail(w, r, rt, err)
		return
	}
	attrs, err := parseAttributes(r.Header, info)
	if err != nil {
		h.fail(w, r, rt, err)
		return
	}
	if err := checkPutRecord(string(key), attrs, writerKeyPrefix+string(a.Key.ID)); err != nil {
		h.fail(w, r, rt, err)
		return
	}
	upload, err := h.deps.Stage.Create(r.Context(), createRequest(rt, info, key, attrs, a))
	if err != nil {
		h.fail(w, r, rt, err)
		return
	}
	setUploadHeaders(w.Header(), attrs)
	h.respond(w, r, rt, initiateResult{Bucket: rt.Target.Bucket, Key: string(key), UploadID: upload.ID})
}

func parseObjectKey(raw string) (object.Key, error) {
	key, err := object.ParseKey(raw)
	if errors.Is(err, object.ErrKeyTooLong) {
		return "", keyTooLong(len(raw))
	}
	return key, err
}

func createRequest(rt router.Route, info bucket.Info, key object.Key, attrs uploadAttributes, a sigv4.Auth) multipartstage.CreateRequest {
	return multipartstage.CreateRequest{
		Bucket:            bucket.BucketName(rt.Target.Bucket),
		BucketChannelID:   info.ChannelID,
		Key:               key,
		ContentType:       attrs.contentType,
		UserMeta:          attrs.userMeta,
		SystemMeta:        attrs.systemMeta,
		Encrypted:         attrs.encrypted,
		ChecksumAlgorithm: attrs.checksumAlgorithm,
		AccessKeyID:       string(a.Key.ID),
	}
}

func setUploadHeaders(header http.Header, attrs uploadAttributes) {
	if attrs.encrypted {
		header.Set(serverSideEncryption, serverSideEncryptionAES)
	}
	if attrs.checksumAlgorithm != "" {
		header.Set(checksumAlgorithmName, attrs.checksumAlgorithm)
	}
	if attrs.checksumType != "" {
		header.Set(checksumTypeName, attrs.checksumType)
	}
}
