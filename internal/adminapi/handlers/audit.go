package handlers

import (
	"context"
	"net/http"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
)

const (
	outcomeSuccess = "success"
	outcomeFailure = "failure"
	outcomePartial = "partial"

	actorDevice    = "device"
	actorAnonymous = "anonymous"
)

func (a *api) actor(ctx context.Context) adminv1.AuditActor {
	device, ok := a.deps.Device(ctx)
	if !ok {
		return adminv1.AuditActor{Type: actorAnonymous}
	}
	return adminv1.AuditActor{Type: actorDevice, DeviceID: device.DeviceID, DeviceName: device.Name}
}

func (a *api) recordAs(r *http.Request, actor adminv1.AuditActor, action, target string, err error) {
	outcome, code := outcomeSuccess, ""
	if err != nil {
		outcome, code = outcomeFailure, string(MapError(err).Body.Code)
	}
	a.writeAudit(r, actor, action, target, outcome, code)
}

func (a *api) record(r *http.Request, action, target string, err error) {
	a.recordAs(r, a.actor(r.Context()), action, target, err)
}

func (a *api) writeAudit(r *http.Request, actor adminv1.AuditActor, action, target, outcome, code string) {
	ctx := context.WithoutCancel(r.Context())
	record := AuditRecord{
		At:         a.deps.Clock.Now().UTC(),
		Actor:      actor,
		Action:     action,
		Target:     target,
		Outcome:    outcome,
		ErrorCode:  code,
		RemoteAddr: a.clientAddress(r),
		RequestID:  RequestIDFromContext(r.Context()),
	}
	if err := a.deps.Audit.Record(ctx, record); err != nil {
		a.deps.Logger.WarnContext(ctx, "admin audit record failed", "action", action, "request_id", record.RequestID, "error", err)
	}
}
