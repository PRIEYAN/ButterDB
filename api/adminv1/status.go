package adminv1

import (
	"time"
)

type Status struct {
	ServerID      string         `json:"server_id"`
	ServerVersion string         `json:"server_version"`
	Commit        string         `json:"commit"`
	StartedAt     time.Time      `json:"started_at"`
	UptimeSeconds int64          `json:"uptime_seconds"`
	State         string         `json:"state"`
	Telegram      TelegramHealth `json:"telegram"`
	Components    []Component    `json:"components"`
	Endpoints     Endpoints      `json:"endpoints"`
	Limits        Limits         `json:"limits"`
	Replica       ReplicaSummary `json:"replica"`
}

type TelegramHealth struct {
	Linked            bool             `json:"linked"`
	Connected         bool             `json:"connected"`
	Account           *TelegramAccount `json:"account"`
	FloodWaitUntil    *time.Time       `json:"flood_wait_until"`
	SendRatePerMinute int              `json:"send_rate_per_minute"`
}

type Component struct {
	Name   string    `json:"name"`
	Status string    `json:"status"`
	Detail string    `json:"detail"`
	Since  time.Time `json:"since"`
}

type Endpoints struct {
	S3PublicURL    string `json:"s3_public_url"`
	S3Region       string `json:"s3_region"`
	MediaPublicURL string `json:"media_public_url"`
}

type Limits struct {
	MaxObjectBytes       int64 `json:"max_object_bytes"`
	MaxKeyBytes          int   `json:"max_key_bytes"`
	MaxUserMetadataBytes int   `json:"max_user_metadata_bytes"`
	MaxPresignSeconds    int   `json:"max_presign_seconds"`
	MaxBuckets           int   `json:"max_buckets"`
	MaxDeleteBatch       int   `json:"max_delete_batch"`
}

type ReplicaSummary struct {
	State      string `json:"state"`
	PendingOps int64  `json:"pending_ops"`
}

type Usage struct {
	Buckets          int        `json:"buckets"`
	Objects          int64      `json:"objects"`
	StoredBytes      int64      `json:"stored_bytes"`
	PlaintextBytes   int64      `json:"plaintext_bytes"`
	EncryptedObjects int64      `json:"encrypted_objects"`
	BlockCache       CacheUsage `json:"block_cache"`
	Spool            SpoolUsage `json:"spool"`
	ComputedAt       time.Time  `json:"computed_at"`
}

type CacheUsage struct {
	UsedBytes  int64   `json:"used_bytes"`
	LimitBytes int64   `json:"limit_bytes"`
	HitRatio1h float64 `json:"hit_ratio_1h"`
}

type SpoolUsage struct {
	UsedBytes    int64 `json:"used_bytes"`
	LimitBytes   int64 `json:"limit_bytes"`
	JobsInFlight int   `json:"jobs_in_flight"`
}
