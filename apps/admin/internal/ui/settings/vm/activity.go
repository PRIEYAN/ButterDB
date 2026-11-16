package vm

import (
	"context"
	"strings"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
)

const (
	AuditPageSize = 100
	actorDevice   = "device"
	actorUnknown  = "unknown"
	auditNoFilter = ""
)

type ActivityState struct {
	Loaded     bool
	Filter     string
	Events     []adminv1.AuditEvent
	NextCursor string
}

func (a ActivityState) HasMore() bool {
	return a.NextCursor != ""
}

func AuditFilters() []string {
	return []string{auditNoFilter, "setup.", "pairing.", "device.", "telegram.", "bucket.", "access_key.", "object.", "replica."}
}

func ActorText(a adminv1.AuditActor) string {
	if a.Type == actorDevice {
		if a.DeviceName != "" {
			return a.DeviceName
		}
		return a.DeviceID
	}
	if a.Type == "" {
		return actorUnknown
	}
	return a.Type
}

func (v *ViewModel) RefreshActivity(ctx context.Context) {
	v.loadActivity(ctx, v.state.Get().Activity.Filter, "")
}

func (v *ViewModel) SetAuditFilter(ctx context.Context, prefix string) {
	v.loadActivity(ctx, normaliseFilter(prefix), "")
}

func (v *ViewModel) LoadMoreActivity(ctx context.Context) {
	a := v.state.Get().Activity
	if !a.HasMore() {
		return
	}
	v.loadActivity(ctx, a.Filter, a.NextCursor)
}

func normaliseFilter(prefix string) string {
	return strings.ToLower(strings.TrimSpace(prefix))
}

func (v *ViewModel) loadActivity(ctx context.Context, filter, cursor string) {
	v.begin()
	page, err := v.client.Audit(ctx, adminv1.AuditQuery{Cursor: cursor, Limit: AuditPageSize, Action: filter})
	if err != nil {
		v.finish(func(*State) {}, v.problemOf(err))
		return
	}
	events := keepPrefix(page.Events, filter)
	v.finish(func(s *State) {
		prior := s.Activity.Events
		if cursor == "" || s.Activity.Filter != filter {
			prior = nil
		}
		s.Activity = ActivityState{Loaded: true, Filter: filter, Events: append(append([]adminv1.AuditEvent(nil), prior...), events...), NextCursor: page.NextCursor}
	}, nil)
}

func keepPrefix(events []adminv1.AuditEvent, prefix string) []adminv1.AuditEvent {
	if prefix == auditNoFilter {
		return events
	}
	kept := make([]adminv1.AuditEvent, 0, len(events))
	for _, e := range events {
		if strings.HasPrefix(e.Action, prefix) {
			kept = append(kept, e)
		}
	}
	return kept
}
