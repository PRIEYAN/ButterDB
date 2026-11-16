package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
)

const (
	jsonBodyLimitBytes = 1 << 20
	jsonContentType    = "application/json; charset=utf-8"
	unknownFieldPrefix = "json: unknown field "
)

type requestIDKey struct{}

func withRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

type trackingWriter struct {
	http.ResponseWriter
	wrote bool
}

func (t *trackingWriter) WriteHeader(status int) {
	t.wrote = true
	t.ResponseWriter.WriteHeader(status)
}

func (t *trackingWriter) Write(p []byte) (int, error) {
	t.wrote = true
	return t.ResponseWriter.Write(p)
}

func (t *trackingWriter) Flush() {
	if flusher, ok := t.ResponseWriter.(http.Flusher); ok {
		t.wrote = true
		flusher.Flush()
	}
}

func (t *trackingWriter) Unwrap() http.ResponseWriter {
	return t.ResponseWriter
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		return failure(adminv1.CodeUnsupportedMediaType, nil)
	}
	body := http.MaxBytesReader(w, r.Body, jsonBodyLimitBytes)
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return classifyDecodeError(err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return &BadRequestError{Field: "body"}
		}
		return classifyDecodeError(err)
	}
	return nil
}

func isJSONContentType(header string) bool {
	mediaType, _, err := mime.ParseMediaType(header)
	return err == nil && mediaType == "application/json"
}

func classifyDecodeError(err error) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return tooLarge
	}
	if name, ok := strings.CutPrefix(err.Error(), unknownFieldPrefix); ok {
		if field, unquoteErr := strconv.Unquote(name); unquoteErr == nil {
			return &BadRequestError{Field: field}
		}
	}
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) && typeErr.Field != "" {
		return &BadRequestError{Field: typeErr.Field}
	}
	return &BadRequestError{Field: "body"}
}

func (a *api) writeJSON(w http.ResponseWriter, r *http.Request, status int, value any) {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(value); err != nil {
		a.writeError(w, r, err)
		return
	}
	header := w.Header()
	header.Set("Content-Type", jsonContentType)
	header.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(buf.Bytes())
}

func (a *api) writeNoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) writeError(w http.ResponseWriter, r *http.Request, err error) {
	mapped := MapError(err)
	mapped.Body.RequestID = RequestIDFromContext(r.Context())
	if errors.Is(err, context.Canceled) {
		a.deps.Logger.DebugContext(r.Context(), "admin api request canceled by the client", "request_id", mapped.Body.RequestID, "method", r.Method, "path", r.URL.Path)
		w.WriteHeader(mapped.Status)
		return
	}
	if mapped.Body.Code == adminv1.CodeInternal {
		a.deps.Logger.ErrorContext(r.Context(), "admin api internal error", "request_id", mapped.Body.RequestID, "method", r.Method, "path", r.URL.Path, "error", err)
	}
	header := w.Header()
	header.Set(adminv1.HeaderErrorCode, string(mapped.Body.Code))
	header.Set("Cache-Control", "no-store")
	if mapped.Body.RetryAfterSeconds > 0 {
		header.Set("Retry-After", strconv.Itoa(mapped.Body.RetryAfterSeconds))
	}
	if r.Method == http.MethodHead {
		w.WriteHeader(mapped.Status)
		return
	}
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(adminv1.ErrorEnvelope{Error: mapped.Body})
	header.Set("Content-Type", jsonContentType)
	w.WriteHeader(mapped.Status)
	_, _ = w.Write(buf.Bytes())
}

type routeHandler func(w http.ResponseWriter, r *http.Request) error

func (a *api) route(h routeHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := h(w, r); err != nil {
			a.writeError(w, r, err)
		}
	})
}
