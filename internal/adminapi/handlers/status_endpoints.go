package handlers

import (
	"net/http"
	"strconv"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
)

const (
	defaultAuditLimit = 100
	maxAuditLimit     = 500
)

func (a *api) registerStatusRoutes() {
	a.authed("GET "+adminv1.PathStatus, a.status)
	a.authed("GET "+adminv1.PathUsage, a.usage)
	a.authed("GET "+adminv1.PathAudit, a.auditPage)
}

func (a *api) status(w http.ResponseWriter, r *http.Request) error {
	status, err := a.deps.Status.Status(r.Context())
	if err != nil {
		return err
	}
	if status.Components == nil {
		status.Components = []adminv1.Component{}
	}
	a.writeJSON(w, r, http.StatusOK, status)
	return nil
}

func (a *api) usage(w http.ResponseWriter, r *http.Request) error {
	usage, err := a.deps.Status.Usage(r.Context())
	if err != nil {
		return err
	}
	a.writeJSON(w, r, http.StatusOK, usage)
	return nil
}

func (a *api) auditPage(w http.ResponseWriter, r *http.Request) error {
	query, err := auditQueryFromRequest(r)
	if err != nil {
		return err
	}
	page, err := a.deps.Audit.Page(r.Context(), query)
	if err != nil {
		return err
	}
	if page.Events == nil {
		page.Events = []adminv1.AuditEvent{}
	}
	a.writeJSON(w, r, http.StatusOK, page)
	return nil
}

func auditQueryFromRequest(r *http.Request) (adminv1.AuditQuery, error) {
	values := r.URL.Query()
	query := adminv1.AuditQuery{
		Cursor: values.Get(adminv1.QueryCursor),
		Action: values.Get(adminv1.QueryAction),
		Limit:  defaultAuditLimit,
	}
	if raw := values.Get(adminv1.QueryLimit); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maxAuditLimit {
			return query, &BadRequestError{Field: adminv1.QueryLimit}
		}
		query.Limit = parsed
	}
	var err error
	if query.Since, err = parseOptionalTime(values.Get(adminv1.QuerySince), adminv1.QuerySince); err != nil {
		return query, err
	}
	query.Until, err = parseOptionalTime(values.Get(adminv1.QueryUntil), adminv1.QueryUntil)
	return query, err
}

func parseOptionalTime(raw, field string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, &BadRequestError{Field: field}
	}
	return parsed.UTC(), nil
}
