package objectops

import (
	"errors"
	"net/http"

	"github.com/PRIEYAN/ButterDB/internal/access/sigv4"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/readpath/httprange"
	"github.com/PRIEYAN/ButterDB/internal/readpath/objectreader"
	"github.com/PRIEYAN/ButterDB/internal/s3api/router"
	"github.com/PRIEYAN/ButterDB/internal/s3api/s3err"
)

func (h *Handlers) GetObject(w http.ResponseWriter, r *http.Request, rt router.Route, a sigv4.Auth) {
	h.serve(w, r, rt, a, false)
}

func (h *Handlers) HeadObject(w http.ResponseWriter, r *http.Request, rt router.Route, a sigv4.Auth) {
	h.serve(w, r, rt, a, true)
}

type readRequest struct {
	info      bucket.Info
	overrides map[string]string
	partOne   bool
}

func (h *Handlers) serve(w http.ResponseWriter, r *http.Request, rt router.Route, a sigv4.Auth, headOnly bool) {
	request, err := h.prepareRead(r, rt, a)
	if err != nil {
		h.fail(w, r, rt, a, err)
		return
	}
	meta, source, err := h.load(r, rt, request.info, headOnly)
	if err != nil {
		h.fail(w, r, rt, a, err)
		return
	}
	if source != nil {
		defer source.Close()
	}
	h.respond(w, r, rt, a, request, meta, source)
}

func (h *Handlers) prepareRead(r *http.Request, rt router.Route, a sigv4.Auth) (readRequest, error) {
	info, err := h.authorizeRead(r, a, rt.Target.Bucket)
	if err != nil {
		return readRequest{}, err
	}
	query := r.URL.Query()
	if err := checkReadFeatures(r.Header, query); err != nil {
		return readRequest{}, err
	}
	overrides, err := responseOverrides(query, a.Kind != sigv4.AuthAnonymous)
	if err != nil {
		return readRequest{}, err
	}
	partOne, err := parsePartNumber(query, r.Header.Get("Range"))
	if err != nil {
		return readRequest{}, err
	}
	return readRequest{info: info, overrides: overrides, partOne: partOne}, nil
}

func (h *Handlers) load(r *http.Request, rt router.Route, info bucket.Info, headOnly bool) (object.ObjectMeta, objectreader.Opened, error) {
	if headOnly {
		meta, err := h.deps.Heads.Get(r.Context(), info.ChannelID, rt.Target.Key)
		return meta, nil, err
	}
	return h.deps.Opener.Open(r.Context(), rt.Target.Bucket, rt.Target.Key)
}

func (h *Handlers) respond(w http.ResponseWriter, r *http.Request, rt router.Route, a sigv4.Auth, request readRequest, meta object.ObjectMeta, source objectreader.Opened) {
	entity := httprange.EntityFromMeta(meta)
	result, err := httprange.Evaluate(httprange.ConditionsFromHeader(r.Header), entity, h.deps.Clock.Now())
	if err != nil {
		h.fail(w, r, rt, a, err)
		return
	}
	if result.NotModified {
		cacheControl := httprange.CacheControl(h.cacheSources(request.info, meta, ""))
		httprange.WriteNotModified(w, entity, cacheControl, meta.SystemMeta["expires"])
		return
	}
	plan, err := h.plan(r, request, meta, result)
	if err != nil {
		h.failRange(w, r, rt, a, meta, err)
		return
	}
	headers := h.responseHeaders(request, meta, plan)
	if source == nil {
		h.writeHeadOnly(w, headers, plan)
		return
	}
	h.stream(w, r, rt, a, headers, plan, source)
}

func (h *Handlers) plan(r *http.Request, request readRequest, meta object.ObjectMeta, result httprange.Result) (httprange.Plan, error) {
	if request.partOne {
		return httprange.PartNumberOnePlan(meta.PlaintextSize), nil
	}
	return httprange.NewPlan(r.Header.Get("Range"), meta.PlaintextSize, result.IgnoreRange)
}

func (h *Handlers) failRange(w http.ResponseWriter, r *http.Request, rt router.Route, a sigv4.Auth, meta object.ObjectMeta, err error) {
	var unsatisfiable *httprange.UnsatisfiableRangeError
	if errors.As(err, &unsatisfiable) {
		httprange.SetUnsatisfiedHeaders(w.Header(), meta.PlaintextSize)
	}
	h.fail(w, r, rt, a, err)
}

func (h *Handlers) cacheSources(info bucket.Info, meta object.ObjectMeta, override string) httprange.CacheControlSources {
	return httprange.CacheControlSources{
		Override:            override,
		Stored:              meta.SystemMeta["cache-control"],
		PublicRead:          info.Policy == bucket.PolicyPublicRead,
		PublicMaxAgeSeconds: int(info.CacheMaxAge),
	}
}

func (h *Handlers) responseHeaders(request readRequest, meta object.ObjectMeta, plan httprange.Plan) http.Header {
	headers := http.Header{}
	contentType := meta.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	headers.Set("Content-Type", contentType)
	httprange.SetValidators(headers, httprange.EntityFromMeta(meta))
	for _, entry := range systemHeaders {
		if value := meta.SystemMeta[entry.name]; value != "" {
			headers.Set(entry.header, value)
		}
	}
	if cacheControl := httprange.CacheControl(h.cacheSources(request.info, meta, request.overrides["Cache-Control"])); cacheControl != "" {
		headers.Set("Cache-Control", cacheControl)
	}
	for header, value := range request.overrides {
		headers.Set(header, value)
	}
	for name, value := range meta.UserMeta {
		headers.Set("X-Amz-Meta-"+name, encodeMetaForResponse(value))
	}
	if meta.Encrypted {
		headers.Set(headerEncryption, encryptionAES256)
	}
	plan.SetHeaders(headers)
	return headers
}

func (h *Handlers) writeHeadOnly(w http.ResponseWriter, headers http.Header, plan httprange.Plan) {
	response := &deferredResponse{w: w, headers: headers, status: plan.Status}
	response.start()
}

func (h *Handlers) stream(w http.ResponseWriter, r *http.Request, rt router.Route, a sigv4.Auth, headers http.Header, plan httprange.Plan, source objectreader.Opened) {
	response := &deferredResponse{w: w, headers: headers, status: plan.Status}
	if plan.Length == 0 {
		response.start()
		return
	}
	err := h.deps.Streamer.Stream(r.Context(), response, source, plan.Range)
	if err == nil {
		return
	}
	if !response.started {
		h.fail(w, r, rt, a, err)
		return
	}
	h.deps.Logger.Warn("s3 object stream aborted", "request_id", w.Header().Get(s3err.HeaderRequestID), "sent", response.written, "expected", plan.Length)
	panic(http.ErrAbortHandler)
}

type deferredResponse struct {
	w       http.ResponseWriter
	headers http.Header
	status  int
	started bool
	written int64
}

func (d *deferredResponse) start() {
	if d.started {
		return
	}
	d.started = true
	destination := d.w.Header()
	for name, values := range d.headers {
		destination[name] = values
	}
	d.w.WriteHeader(d.status)
}

func (d *deferredResponse) Write(p []byte) (int, error) {
	d.start()
	n, err := d.w.Write(p)
	d.written += int64(n)
	return n, err
}
