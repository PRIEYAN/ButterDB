package core

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/internal/adminapi/handlers"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/storage/adminrepo"
)

const (
	authFailureAction     = "auth.failure"
	authFailureAuditEvery = time.Minute
	authFailureTrackedMax = 4096
	authFailureBodyLimit  = 2048
	auditCursorSeparator  = "."
	auditActorAnonymous   = "anonymous"
)

var _ handlers.AuditLog = (*auditLog)(nil)

type auditRepository interface {
	InsertAudit(ctx context.Context, e adminrepo.AuditEntry) (int64, error)
	PageAudit(ctx context.Context, f adminrepo.AuditFilter) (adminrepo.AuditPage, error)
}

type auditLog struct {
	repo auditRepository
}

func (a *auditLog) Record(ctx context.Context, e handlers.AuditRecord) error {
	_, err := a.repo.InsertAudit(ctx, adminrepo.AuditEntry{
		At:            e.At,
		ActorType:     e.Actor.Type,
		ActorDeviceID: e.Actor.DeviceID,
		Action:        e.Action,
		Target:        e.Target,
		Outcome:       e.Outcome,
		ErrorCode:     e.ErrorCode,
		RemoteAddr:    e.RemoteAddr,
		RequestID:     e.RequestID,
	})
	return err
}

func (a *auditLog) Page(ctx context.Context, q adminv1.AuditQuery) (adminv1.AuditPage, error) {
	after, err := decodeAuditCursor(q.Cursor)
	if err != nil {
		return adminv1.AuditPage{}, err
	}
	page, err := a.repo.PageAudit(ctx, adminrepo.AuditFilter{
		After: after, Limit: q.Limit, Action: q.Action, Since: q.Since, Until: q.Until,
	})
	if err != nil {
		return adminv1.AuditPage{}, err
	}
	events := make([]adminv1.AuditEvent, 0, len(page.Entries))
	for _, entry := range page.Entries {
		events = append(events, auditEventOf(entry))
	}
	result := adminv1.AuditPage{Events: events}
	if page.HasMore {
		result.NextCursor = encodeAuditCursor(page.Next)
	}
	return result, nil
}

func auditEventOf(entry adminrepo.AuditEntry) adminv1.AuditEvent {
	return adminv1.AuditEvent{
		ID:         entry.ID,
		At:         entry.At.UTC(),
		Actor:      adminv1.AuditActor{Type: entry.ActorType, DeviceID: entry.ActorDeviceID, DeviceName: entry.ActorDeviceName},
		Action:     entry.Action,
		Target:     entry.Target,
		Outcome:    entry.Outcome,
		ErrorCode:  entry.ErrorCode,
		RemoteAddr: entry.RemoteAddr,
		RequestID:  entry.RequestID,
	}
}

func encodeAuditCursor(cursor adminrepo.AuditCursor) string {
	text := strconv.FormatInt(cursor.At.UnixMilli(), 10) + auditCursorSeparator + strconv.FormatInt(cursor.ID, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(text))
}

func decodeAuditCursor(raw string) (adminrepo.AuditCursor, error) {
	if raw == "" {
		return adminrepo.AuditCursor{}, nil
	}
	invalid := &handlers.BadRequestError{Field: adminv1.QueryCursor}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return adminrepo.AuditCursor{}, invalid
	}
	millis, id, found := strings.Cut(string(decoded), auditCursorSeparator)
	if !found {
		return adminrepo.AuditCursor{}, invalid
	}
	at, atErr := strconv.ParseInt(millis, 10, 64)
	rowID, idErr := strconv.ParseInt(id, 10, 64)
	if atErr != nil || idErr != nil || at < 0 || rowID < 1 {
		return adminrepo.AuditCursor{}, invalid
	}
	return adminrepo.AuditCursor{At: time.UnixMilli(at).UTC(), ID: rowID}, nil
}

type authFailureAuditor struct {
	audit  auditRepository
	clock  clock.Clock
	logger *slog.Logger

	mu       sync.Mutex
	recorded map[string]time.Time
}

func newAuthFailureAuditor(audit auditRepository, clk clock.Clock, logger *slog.Logger) *authFailureAuditor {
	return &authFailureAuditor{audit: audit, clock: clk, logger: logger, recorded: make(map[string]time.Time)}
}

func (a *authFailureAuditor) wrap(authenticate func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reached := false
			guarded := authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reached = true
				next.ServeHTTP(w, r)
			}))
			capture := &statusCapture{ResponseWriter: w}
			guarded.ServeHTTP(capture, r)
			if !reached && capture.status == http.StatusUnauthorized {
				a.record(r, w.Header().Get(adminv1.HeaderRequestID), capture.errorCode())
			}
		})
	}
}

func (a *authFailureAuditor) record(r *http.Request, requestID, code string) {
	addr := clientAddressOf(r.Context())
	if addr == "" {
		addr = peerHost(r.RemoteAddr)
	}
	now := a.clock.Now()
	if !a.admit(addr, now) {
		return
	}
	ctx := context.WithoutCancel(r.Context())
	_, err := a.audit.InsertAudit(ctx, adminrepo.AuditEntry{
		At:         now.UTC(),
		ActorType:  auditActorAnonymous,
		Action:     authFailureAction,
		Outcome:    adminrepo.AuditOutcomeFailure,
		ErrorCode:  code,
		RemoteAddr: addr,
		RequestID:  requestID,
	})
	if err != nil {
		a.logger.WarnContext(ctx, "admin audit record failed", "action", authFailureAction, "request_id", requestID, "error", err)
	}
}

func (a *authFailureAuditor) admit(addr string, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if last, seen := a.recorded[addr]; seen && now.Sub(last) < authFailureAuditEvery {
		return false
	}
	if len(a.recorded) >= authFailureTrackedMax {
		for key, at := range a.recorded {
			if now.Sub(at) >= authFailureAuditEvery {
				delete(a.recorded, key)
			}
		}
	}
	a.recorded[addr] = now
	return true
}

func peerHost(remote string) string {
	if index := strings.LastIndex(remote, ":"); index > 0 {
		return strings.Trim(remote[:index], "[]")
	}
	return remote
}

type statusCapture struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (s *statusCapture) WriteHeader(status int) {
	if s.status == 0 {
		s.status = status
	}
	s.ResponseWriter.WriteHeader(status)
}

func (s *statusCapture) Write(p []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	if room := authFailureBodyLimit - s.body.Len(); s.status == http.StatusUnauthorized && room > 0 {
		s.body.Write(p[:min(len(p), room)])
	}
	return s.ResponseWriter.Write(p)
}

func (s *statusCapture) Flush() {
	if flusher, ok := s.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (s *statusCapture) Unwrap() http.ResponseWriter {
	return s.ResponseWriter
}

func (s *statusCapture) errorCode() string {
	var envelope adminv1.ErrorEnvelope
	if err := json.Unmarshal(s.body.Bytes(), &envelope); err != nil || envelope.Error.Code == "" {
		return string(adminv1.CodeUnauthenticated)
	}
	return string(envelope.Error.Code)
}
