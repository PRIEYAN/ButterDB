package core

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/internal/adminapi/handlers"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/readpath/httprange"
	"github.com/PRIEYAN/ButterDB/internal/readpath/objectreader"
)

const (
	defaultDownloadContentType = "application/octet-stream"
	dispositionAttrChars       = "!#$&+-.^_`|~"
)

var _ handlers.ObjectServer = (*adminObjectServer)(nil)

type adminObjectServer struct {
	gate     backendGate
	streamer *httprange.Streamer
	clock    clock.Clock
}

func newAdminObjectServer(gate backendGate, clk clock.Clock) *adminObjectServer {
	return &adminObjectServer{gate: gate, streamer: httprange.NewStreamer(), clock: clk}
}

func (s *adminObjectServer) ServeObject(w http.ResponseWriter, r *http.Request, name bucket.BucketName, key object.Key) error {
	backend, err := s.gate.backend(r.Context())
	if err != nil {
		return err
	}
	headOnly := r.Method == http.MethodHead
	meta, source, err := s.load(r, backend, name, key, headOnly)
	if err != nil {
		return err
	}
	if source != nil {
		defer source.Close()
	}
	return s.respond(w, r, meta, source)
}

func (s *adminObjectServer) load(r *http.Request, backend *Backend, name bucket.BucketName, key object.Key, headOnly bool) (object.ObjectMeta, objectreader.Opened, error) {
	if headOnly {
		info, err := s.gate.bucketInfo(r.Context(), backend, name)
		if err != nil {
			return object.ObjectMeta{}, nil, err
		}
		meta, err := backend.Read.Live.Get(r.Context(), info.ChannelID, string(key))
		return meta, nil, err
	}
	meta, opened, err := backend.Read.Open(r.Context(), string(name), string(key))
	if err != nil {
		return object.ObjectMeta{}, nil, err
	}
	return meta, opened, nil
}

func (s *adminObjectServer) respond(w http.ResponseWriter, r *http.Request, meta object.ObjectMeta, source objectreader.Opened) error {
	entity := httprange.EntityFromMeta(meta)
	result, err := httprange.Evaluate(httprange.ConditionsFromHeader(r.Header), entity, s.clock.Now())
	if err != nil {
		return err
	}
	if result.NotModified {
		httprange.WriteNotModified(w, entity, "", "")
		return nil
	}
	plan, err := httprange.NewPlan(r.Header.Get("Range"), meta.PlaintextSize, result.IgnoreRange)
	if err != nil {
		var unsatisfiable *httprange.UnsatisfiableRangeError
		if errors.As(err, &unsatisfiable) {
			httprange.SetUnsatisfiedHeaders(w.Header(), meta.PlaintextSize)
		}
		return err
	}
	setDownloadHeaders(w.Header(), meta, plan)
	w.WriteHeader(plan.Status)
	if source == nil || plan.Length == 0 {
		return nil
	}
	if err := s.streamer.Stream(r.Context(), w, source, plan.Range); err != nil {
		return fmt.Errorf("core: admin: stream object: %w", err)
	}
	return nil
}

func setDownloadHeaders(header http.Header, meta object.ObjectMeta, plan httprange.Plan) {
	contentType := meta.ContentType
	if contentType == "" {
		contentType = defaultDownloadContentType
	}
	header.Set("Content-Type", contentType)
	httprange.SetValidators(header, httprange.EntityFromMeta(meta))
	header.Set("Content-Disposition", "attachment; filename*=UTF-8''"+dispositionFilename(meta.Key))
	header.Set(adminv1.HeaderEncrypted, boolText(meta.Encrypted))
	header.Set(adminv1.HeaderVersionID, fmt.Sprintf("%d", meta.PutMsgID))
	for name, value := range meta.UserMeta {
		header.Set(adminv1.HeaderMetaPrefix+name, value)
	}
	plan.SetHeaders(header)
}

func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func dispositionFilename(key string) string {
	segment := key[strings.LastIndex(key, "/")+1:]
	var out strings.Builder
	for i := 0; i < len(segment); i++ {
		c := segment[i]
		if isDispositionByte(c) {
			out.WriteByte(c)
			continue
		}
		fmt.Fprintf(&out, "%%%02X", c)
	}
	return out.String()
}

func isDispositionByte(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte(dispositionAttrChars, c) >= 0
}
