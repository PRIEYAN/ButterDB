package handlers

import (
	"errors"
	"net/http"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/internal/domain/access"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
)

const (
	defaultPresignSeconds = 3600
	maxPresignSeconds     = 604800
)

func (a *api) presignObject(w http.ResponseWriter, r *http.Request) error {
	name, err := bucketParam(r)
	if err != nil {
		return err
	}
	var req adminv1.PresignRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return err
	}
	key, err := object.ParseKey(req.Key)
	if err != nil {
		return err
	}
	seconds := req.ExpiresInSeconds
	if seconds == 0 {
		seconds = defaultPresignSeconds
	}
	if seconds < 1 || seconds > maxPresignSeconds {
		return &ValidationError{Field: "expires_in_seconds", Reason: "must be 1 to 604800"}
	}
	id, err := a.presignKey(r, name, req.AccessKeyID)
	if err != nil {
		return err
	}
	if _, err := a.deps.Lister.Meta(r.Context(), name, key); err != nil {
		return err
	}
	url, expiresAt, err := a.deps.Presign.PresignGet(r.Context(), id, name, key, time.Duration(seconds)*time.Second)
	a.record(r, "object.presign", objectTarget(name, key), err)
	if err != nil {
		return err
	}
	a.writeJSON(w, r, http.StatusOK, adminv1.PresignedURL{URL: url, Method: http.MethodGet, ExpiresAt: utcMillis(expiresAt)})
	return nil
}

func (a *api) presignKey(r *http.Request, name bucket.BucketName, raw string) (access.AccessKeyID, error) {
	id, err := access.ParseAccessKeyID(raw)
	if err != nil {
		return "", failure(adminv1.CodePresignKeyInvalid, nil)
	}
	info, err := a.deps.Keys.Get(r.Context(), id)
	if errors.Is(err, errs.ErrAccessKeyNotFound) || errors.Is(err, errs.ErrUnknownAccessKey) {
		return "", failure(adminv1.CodePresignKeyInvalid, nil)
	}
	if err != nil {
		return "", err
	}
	if info.Revoked() || !info.Scope.Allows(string(name), access.ActionRead) {
		return "", failure(adminv1.CodePresignKeyInvalid, nil)
	}
	return id, nil
}
