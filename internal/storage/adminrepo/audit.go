package adminrepo

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const (
	AuditOutcomeSuccess = "success"
	AuditOutcomeFailure = "failure"
	DefaultAuditLimit   = 100
	MaxAuditLimit       = 500
)

type AuditEntry struct {
	ID              int64
	At              time.Time
	ActorType       string
	ActorDeviceID   string
	ActorDeviceName string
	Action          string
	Target          string
	Outcome         string
	ErrorCode       string
	RemoteAddr      string
	RequestID       string
}

type AuditCursor struct {
	At time.Time
	ID int64
}

type AuditFilter struct {
	After  AuditCursor
	Limit  int
	Action string
	Since  time.Time
	Until  time.Time
}

type AuditPage struct {
	Entries []AuditEntry
	Next    AuditCursor
	HasMore bool
}

const insertAudit = `INSERT INTO admin_audit (at, actor_type, actor_device_id, action, target, outcome, error_code, remote_addr, request_id)
VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9)`

const pageAuditHead = `SELECT a.id, a.at, a.actor_type, a.actor_device_id, COALESCE(d.name, ''), a.action, a.target,
  a.outcome, a.error_code, a.remote_addr, a.request_id
FROM admin_audit a LEFT JOIN admin_devices d ON d.device_id = a.actor_device_id AND a.actor_device_id != ''`

func (r *Repository) InsertAudit(ctx context.Context, e AuditEntry) (int64, error) {
	if e.At.IsZero() {
		e.At = r.clock.Now()
	}
	var id int64
	err := r.db.Write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, insertAudit, millis(e.At), e.ActorType, e.ActorDeviceID, e.Action,
			e.Target, e.Outcome, e.ErrorCode, e.RemoteAddr, e.RequestID)
		if err != nil {
			return err
		}
		id, err = result.LastInsertId()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("adminrepo: insert audit: %w", err)
	}
	return id, nil
}

func (r *Repository) PageAudit(ctx context.Context, f AuditFilter) (AuditPage, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultAuditLimit
	}
	if limit > MaxAuditLimit {
		limit = MaxAuditLimit
	}
	query, args := buildAuditQuery(f, limit+1)
	var page AuditPage
	err := r.db.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			e, err := scanAudit(rows)
			if err != nil {
				return err
			}
			page.Entries = append(page.Entries, e)
		}
		return rows.Err()
	})
	if err != nil {
		return AuditPage{}, fmt.Errorf("adminrepo: page audit: %w", err)
	}
	if len(page.Entries) > limit {
		page.Entries = page.Entries[:limit]
		page.HasMore = true
	}
	if page.HasMore {
		last := page.Entries[len(page.Entries)-1]
		page.Next = AuditCursor{At: last.At, ID: last.ID}
	}
	return page, nil
}

func buildAuditQuery(f AuditFilter, fetch int) (string, []any) {
	var conditions []string
	var args []any
	add := func(condition string, values ...any) {
		conditions = append(conditions, condition)
		args = append(args, values...)
	}
	if f.After.ID != 0 || !f.After.At.IsZero() {
		add("(a.at < ? OR (a.at = ? AND a.id < ?))", millis(f.After.At), millis(f.After.At), f.After.ID)
	}
	if !f.Since.IsZero() {
		add("a.at >= ?", millis(f.Since))
	}
	if !f.Until.IsZero() {
		add("a.at <= ?", millis(f.Until))
	}
	if strings.HasSuffix(f.Action, ".") {
		add("a.action >= ? AND a.action < ?", f.Action, strings.TrimSuffix(f.Action, ".")+"/")
	} else if f.Action != "" {
		add("a.action = ?", f.Action)
	}
	query := pageAuditHead
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY a.at DESC, a.id DESC LIMIT ?"
	return query, append(args, fetch)
}

func scanAudit(row rowScanner) (AuditEntry, error) {
	var (
		e  AuditEntry
		at int64
	)
	err := row.Scan(&e.ID, &at, &e.ActorType, &e.ActorDeviceID, &e.ActorDeviceName, &e.Action, &e.Target,
		&e.Outcome, &e.ErrorCode, &e.RemoteAddr, &e.RequestID)
	if err != nil {
		return AuditEntry{}, err
	}
	e.At = fromMillis(at)
	return e, nil
}

func (r *Repository) PruneAudit(ctx context.Context, olderThan time.Time, maxRows int) (int, error) {
	var deleted int
	err := r.db.Write(ctx, func(tx *sql.Tx) error {
		byAge, err := tx.ExecContext(ctx, `DELETE FROM admin_audit WHERE at < ?1`, millis(olderThan))
		if err != nil {
			return err
		}
		byRows, err := tx.ExecContext(ctx,
			`DELETE FROM admin_audit WHERE id NOT IN (SELECT id FROM admin_audit ORDER BY id DESC LIMIT ?1)`, maxRows)
		if err != nil {
			return err
		}
		for _, result := range []sql.Result{byAge, byRows} {
			affected, err := result.RowsAffected()
			if err != nil {
				return err
			}
			deleted += int(affected)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("adminrepo: prune audit: %w", err)
	}
	return deleted, nil
}
