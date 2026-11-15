package s3err

import (
	"net/http"
	"strconv"
	"time"
)

func Write(w http.ResponseWriter, r *http.Request, e APIError) {
	if e.Status == 0 {
		e.Status = StatusOf(e.Code)
	}
	requestID, hostID := RequestIDs(w.Header())
	header := w.Header()
	header.Set(HeaderRequestID, requestID)
	header.Set(HeaderHostID, hostID)
	header.Set("X-Content-Type-Options", "nosniff")
	if header.Get("Server") == "" {
		header.Set("Server", "butterdb")
	}
	if e.RetryAfter > 0 {
		header.Set("Retry-After", strconv.FormatInt(wholeSeconds(e.RetryAfter), 10))
	}
	if e.Resource == "" && r != nil && e.BucketName == "" {
		e.Resource = r.URL.EscapedPath()
	}
	if bodyless(r, e.Status) {
		w.WriteHeader(e.Status)
		return
	}
	body := append([]byte(xmlDeclaration), Encode(e, requestID, hostID)...)
	header.Set("Content-Type", "application/xml")
	header.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(e.Status)
	_, _ = w.Write(body)
}

func bodyless(r *http.Request, status int) bool {
	if status == http.StatusNotModified {
		return true
	}
	return r != nil && r.Method == http.MethodHead
}

func wholeSeconds(d time.Duration) int64 {
	seconds := int64((d + time.Second - 1) / time.Second)
	return max(seconds, 1)
}
