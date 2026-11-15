package objectops

import (
	"context"
	"net/http"

	"github.com/PRIEYAN/ButterDB/internal/access/sigv4"
	"github.com/PRIEYAN/ButterDB/internal/domain/access"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/s3api/router"
	"github.com/PRIEYAN/ButterDB/internal/s3api/s3err"
)

func (h *Handlers) DeleteObject(w http.ResponseWriter, r *http.Request, rt router.Route, a sigv4.Auth) {
	if _, err := h.authorize(r, a, rt.Target.Bucket, access.ActionDelete); err != nil {
		h.fail(w, r, rt, a, err)
		return
	}
	if err := checkVersionID(r.URL.Query()); err != nil {
		h.fail(w, r, rt, a, err)
		return
	}
	if err := validateKey(rt.Target.Key); err != nil {
		h.fail(w, r, rt, a, err)
		return
	}
	ctx, err := h.callerContext(w, r, a)
	if err != nil {
		h.fail(w, r, rt, a, err)
		return
	}
	if err := h.deps.Remover.Delete(ctx, bucket.BucketName(rt.Target.Bucket), object.Key(rt.Target.Key)); err != nil {
		h.fail(w, r, rt, a, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) callerContext(w http.ResponseWriter, r *http.Request, a sigv4.Auth) (context.Context, error) {
	return h.deps.Principals.WithPrincipal(commitContext(r), string(a.Key.ID), w.Header().Get(s3err.HeaderRequestID))
}
