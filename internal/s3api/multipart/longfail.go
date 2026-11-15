package multipart

import (
	"net/http"

	"github.com/PRIEYAN/ButterDB/internal/s3api/router"
	"github.com/PRIEYAN/ButterDB/internal/s3api/s3err"
	"github.com/PRIEYAN/ButterDB/internal/s3api/xmlcodec"
)

func (h *Handlers) failLong(w http.ResponseWriter, rt router.Route, long *xmlcodec.LongResponse, err error) {
	api := s3err.Map(normalizeError(err))
	api.BucketName = rt.Target.Bucket
	if api.Code == s3err.CodeNoSuchUpload || api.Code == s3err.CodeNoSuchKey {
		api.Key = rt.Target.Key
	}
	if api.Resource == "" {
		api.Resource = "/" + rt.Target.Bucket + "/" + rt.Target.Key
	}
	requestID, hostID := s3err.RequestIDs(w.Header())
	_ = long.Fail(s3err.Encode(api, requestID, hostID))
}
