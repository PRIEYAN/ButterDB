package multipart

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/access/policy"
	"github.com/PRIEYAN/ButterDB/internal/access/sigv4"
	"github.com/PRIEYAN/ButterDB/internal/domain/access"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/s3api/router"
	"github.com/PRIEYAN/ButterDB/internal/s3api/s3err"
	"github.com/PRIEYAN/ButterDB/internal/s3api/xmlcodec"
)

const (
	defaultKeepaliveInterval = 15 * time.Second
	ownerDisplayName         = "butterdb"
	storageClassStandard     = "STANDARD"
	serverSideEncryption     = "X-Amz-Server-Side-Encryption"
	serverSideEncryptionAES  = "AES256"
	abortBusyText            = "A conflicting conditional operation is currently in progress against this resource. Please try again."
	uploadIDQuery            = "uploadId"
	partNumberQuery          = "partNumber"

	stateOpen       = "open"
	stateCommitting = "committing"
	stateCompleted  = "completed"
	stateAborted    = "aborted"
)

type Config struct {
	KeepaliveInterval time.Duration
	BodyIdleTimeout   time.Duration
	PublicURL         string
	OwnerID           string
}

type Dependencies struct {
	Stage      Stage
	Verifier   PayloadOpener
	Clock      clock.Clock
	Config     Config
	Authorizer BucketAuthorizer
	Uploads    UploadLookup
	Sources    SourceReader
}

type Handlers struct {
	deps Dependencies
}

func NewHandlers(deps Dependencies) (*Handlers, error) {
	switch {
	case deps.Stage == nil:
		return nil, fmt.Errorf("multipart: %w: stage", errs.ErrMissingDependency)
	case deps.Verifier == nil:
		return nil, fmt.Errorf("multipart: %w: verifier", errs.ErrMissingDependency)
	case deps.Clock == nil:
		return nil, fmt.Errorf("multipart: %w: clock", errs.ErrMissingDependency)
	case deps.Authorizer == nil:
		return nil, fmt.Errorf("multipart: %w: authorizer", errs.ErrMissingDependency)
	case deps.Uploads == nil:
		return nil, fmt.Errorf("multipart: %w: uploads", errs.ErrMissingDependency)
	case deps.Sources == nil:
		return nil, fmt.Errorf("multipart: %w: sources", errs.ErrMissingDependency)
	case deps.Config.OwnerID == "":
		return nil, fmt.Errorf("multipart: %w: owner id", errs.ErrMissingDependency)
	}
	if deps.Config.KeepaliveInterval <= 0 {
		deps.Config.KeepaliveInterval = defaultKeepaliveInterval
	}
	return &Handlers{deps: deps}, nil
}

func (h *Handlers) Routes() map[router.Operation]router.HandlerFunc {
	return map[router.Operation]router.HandlerFunc{
		router.OpCreateMultipartUpload: h.CreateMultipartUpload,
		router.OpUploadPart:            h.UploadPart,
		router.OpUploadPartCopy:        h.UploadPartCopy,
		router.OpAbortMultipartUpload:  h.AbortMultipartUpload,
	}
}

func (h *Handlers) authorize(r *http.Request, rt router.Route, a sigv4.Auth, action access.Action) (bucket.Info, error) {
	if a.Kind == sigv4.AuthAnonymous {
		return bucket.Info{}, errs.ErrAccessDenied
	}
	return h.authorizeBucket(r.Context(), a, rt.Target.Bucket, action)
}

func (h *Handlers) authorizeBucket(ctx context.Context, a sigv4.Auth, bucketName string, action access.Action) (bucket.Info, error) {
	return h.deps.Authorizer.Authorize(ctx, policy.Principal{Key: a.Key}, bucket.BucketName(bucketName), action)
}

func (h *Handlers) fail(w http.ResponseWriter, r *http.Request, rt router.Route, err error) {
	router.WriteError(w, r, rt.Target, normalizeError(err))
}

func (h *Handlers) failWith(w http.ResponseWriter, r *http.Request, rt router.Route, api s3err.APIError) {
	router.WriteError(w, r, rt.Target, api)
}

func (h *Handlers) respond(w http.ResponseWriter, r *http.Request, rt router.Route, v any) {
	if err := xmlcodec.WriteResult(w, http.StatusOK, v); err != nil {
		h.fail(w, r, rt, err)
	}
}

func normalizeError(err error) error {
	switch {
	case errors.Is(err, os.ErrDeadlineExceeded):
		return s3err.New(s3err.CodeRequestTimeout, "")
	case errors.Is(err, io.ErrUnexpectedEOF):
		return s3err.New(s3err.CodeIncompleteBody, "")
	}
	return err
}

func (h *Handlers) lookupUpload(ctx context.Context, rt router.Route, uploadID string) (UploadRecord, error) {
	record, err := h.deps.Uploads.LookupUpload(ctx, uploadID)
	if err != nil {
		return UploadRecord{}, err
	}
	if record.Bucket != rt.Target.Bucket || record.Key != rt.Target.Key {
		return UploadRecord{}, fmt.Errorf("multipart: upload %s: %w", uploadID, errs.ErrNoSuchUpload)
	}
	return record, nil
}

func requireOpenUpload(record UploadRecord) error {
	switch record.State {
	case stateOpen:
		return nil
	case stateCommitting:
		return fmt.Errorf("multipart: upload %s: %w", record.ID, errs.ErrUploadBusy)
	}
	return fmt.Errorf("multipart: upload %s: %w", record.ID, errs.ErrNoSuchUpload)
}
