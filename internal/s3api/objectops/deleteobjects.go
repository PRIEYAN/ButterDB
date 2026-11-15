package objectops

import (
	"context"
	"encoding/xml"
	"net/http"

	"github.com/PRIEYAN/ButterDB/internal/access/policy"
	"github.com/PRIEYAN/ButterDB/internal/access/sigv4"
	"github.com/PRIEYAN/ButterDB/internal/domain/access"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/limits"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/s3api/router"
	"github.com/PRIEYAN/ButterDB/internal/s3api/s3err"
	"github.com/PRIEYAN/ButterDB/internal/s3api/xmlcodec"
)

const deleteObjectsBodyLimit = 2 << 20

type deleteRequest struct {
	XMLName xml.Name       `xml:"Delete"`
	Quiet   bool           `xml:"Quiet"`
	Objects []deleteTarget `xml:"Object"`
}

type deleteTarget struct {
	Key       string `xml:"Key"`
	VersionID string `xml:"VersionId"`
}

type deletedEntry struct {
	Key string `xml:"Key"`
}

type deleteErrorEntry struct {
	Key     string `xml:"Key"`
	Code    string `xml:"Code"`
	Message string `xml:"Message"`
}

type deleteResult struct {
	XMLName xml.Name           `xml:"DeleteResult"`
	Deleted []deletedEntry     `xml:"Deleted"`
	Errors  []deleteErrorEntry `xml:"Error"`
}

func (h *Handlers) DeleteObjects(w http.ResponseWriter, r *http.Request, rt router.Route, a sigv4.Auth) {
	if _, err := h.authorize(r, a, rt.Target.Bucket, access.ActionDelete); err != nil {
		h.fail(w, r, rt, a, err)
		return
	}
	request, err := h.readDeleteRequest(w, r, a)
	if err != nil {
		h.fail(w, r, rt, a, err)
		return
	}
	ctx, err := h.callerContext(w, r, a)
	if err != nil {
		h.fail(w, r, rt, a, err)
		return
	}
	result := h.deleteEach(ctx, r, a, rt.Target.Bucket, request)
	if err := xmlcodec.WriteResult(w, http.StatusOK, result); err != nil {
		h.fail(w, r, rt, a, err)
	}
}

func (h *Handlers) readDeleteRequest(w http.ResponseWriter, r *http.Request, a sigv4.Auth) (deleteRequest, error) {
	sum, err := parseContentMD5(r.Header.Get(headerContentMD5))
	if err != nil {
		return deleteRequest{}, err
	}
	payload, err := h.deps.Payloads.Body(r, a)
	if err != nil {
		return deleteRequest{}, err
	}
	defer payload.Close()
	body := h.withIdleDeadline(w, r, newBodyCloser(newMD5VerifyingBody(payload, sum), payload))
	var request deleteRequest
	if err := xmlcodec.Decode(body, deleteObjectsBodyLimit, &request); err != nil {
		return deleteRequest{}, err
	}
	if len(request.Objects) == 0 || len(request.Objects) > limits.MaxDeleteObjectsKeys {
		return deleteRequest{}, s3err.New(s3err.CodeMalformedXML, "")
	}
	return request, nil
}

func (h *Handlers) deleteEach(ctx context.Context, r *http.Request, a sigv4.Auth, bucketName string, request deleteRequest) deleteResult {
	var result deleteResult
	slowDown := false
	for _, target := range request.Objects {
		if r.Context().Err() != nil {
			break
		}
		err := h.deleteOne(ctx, a, bucketName, target, slowDown)
		if err == nil {
			if !request.Quiet {
				result.Deleted = append(result.Deleted, deletedEntry{Key: target.Key})
			}
			continue
		}
		api := s3err.Map(err)
		slowDown = slowDown || api.Code == s3err.CodeSlowDown
		result.Errors = append(result.Errors, deleteErrorEntry{Key: target.Key, Code: string(api.Code), Message: api.Message})
	}
	return result
}

func (h *Handlers) deleteOne(ctx context.Context, a sigv4.Auth, bucketName string, target deleteTarget, budgetSpent bool) error {
	if err := validateKey(target.Key); err != nil {
		return err
	}
	if target.VersionID != "" && target.VersionID != versionIDNull {
		return invalidArgument(invalidVersionIDText)
	}
	if budgetSpent {
		return s3err.New(s3err.CodeSlowDown, "")
	}
	_, err := h.deps.Evaluator.Authorize(ctx, policy.Principal{Key: a.Key}, bucket.BucketName(bucketName), access.ActionDelete)
	if err != nil {
		return err
	}
	if err := h.deps.Remover.Delete(ctx, bucket.BucketName(bucketName), object.Key(target.Key)); err != nil {
		return err
	}
	return nil
}
