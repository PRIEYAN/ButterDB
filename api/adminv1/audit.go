package adminv1

import (
	"time"
)

type AuditActor struct {
	Type       string `json:"type"`
	DeviceID   string `json:"device_id,omitempty"`
	DeviceName string `json:"device_name,omitempty"`
}

type AuditEvent struct {
	ID         int64      `json:"id"`
	At         time.Time  `json:"at"`
	Actor      AuditActor `json:"actor"`
	Action     string     `json:"action"`
	Target     string     `json:"target"`
	Outcome    string     `json:"outcome"`
	ErrorCode  string     `json:"error_code"`
	RemoteAddr string     `json:"remote_addr"`
	RequestID  string     `json:"request_id"`
}

type AuditPage struct {
	Events     []AuditEvent `json:"events"`
	NextCursor string       `json:"next_cursor,omitempty"`
}

type AuditQuery struct {
	Cursor string
	Limit  int
	Action string
	Since  time.Time
	Until  time.Time
}
