package multipart

import (
	"errors"
	"net/http"

	"github.com/PRIEYAN/ButterDB/internal/access/sigv4"
	"github.com/PRIEYAN/ButterDB/internal/domain/access"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/s3api/router"
	"github.com/PRIEYAN/ButterDB/internal/s3api/s3err"
)

func (h *Handlers) AbortMultipartUpload(w http.ResponseWriter, r *http.Request, rt router.Route, a sigv4.Auth) {
	if _, err := h.authorize(r, rt, a, access.ActionWrite); err != nil {
		h.fail(w, r, rt, err)
		return
	}
	record, err := h.lookupRequestedUpload(r, rt)
	if err != nil {
		h.fail(w, r, rt, err)
		return
	}
	if err := requireOpenUpload(record); err != nil {
		h.fail(w, r, rt, abortError(err))
		return
	}
	if err := h.deps.Stage.Abort(r.Context(), record.ID); err != nil {
		h.fail(w, r, rt, abortError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func abortError(err error) error {
	if errors.Is(err, errs.ErrUploadBusy) {
		return s3err.New(s3err.CodeOperationAborted, abortBusyText)
	}
	return err
}
