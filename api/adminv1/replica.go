package adminv1

import (
	"time"
)

type ReplicaStatus struct {
	State             string           `json:"state"`
	Next              TelegramNext     `json:"next"`
	CredentialsSource string           `json:"credentials_source"`
	Login             *TelegramLogin   `json:"login"`
	Account           *TelegramAccount `json:"account"`
	Lag               ReplicaLag       `json:"lag"`
	Backfill          ReplicaBackfill  `json:"backfill"`
	LastError         *ErrorBody       `json:"last_error"`
}

type ReplicaLag struct {
	PendingOps      int64      `json:"pending_ops"`
	OldestPendingAt *time.Time `json:"oldest_pending_at"`
}

type ReplicaBackfill struct {
	ObjectsTotal int64      `json:"objects_total"`
	ObjectsDone  int64      `json:"objects_done"`
	BytesDone    int64      `json:"bytes_done"`
	StartedAt    *time.Time `json:"started_at"`
}

type ReplicaLoginStartRequest struct {
	Phone   string `json:"phone"`
	APIID   int    `json:"api_id"`
	APIHash string `json:"api_hash"`
}
