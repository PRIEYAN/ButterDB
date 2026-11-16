package adminv1

import (
	"time"
)

type Bucket struct {
	Name               string    `json:"name"`
	CreatedAt          time.Time `json:"created_at"`
	Policy             string    `json:"policy"`
	EncryptionDefault  string    `json:"encryption_default"`
	CacheMaxAgeSeconds int       `json:"cache_max_age_seconds"`
	ObjectCount        int64     `json:"object_count"`
	StoredBytes        int64     `json:"stored_bytes"`
	TelegramChannelID  int64     `json:"telegram_channel_id"`
}

type BucketList struct {
	Buckets []Bucket `json:"buckets"`
}

type CreateBucketRequest struct {
	Name               string `json:"name"`
	Policy             string `json:"policy,omitempty"`
	EncryptionDefault  string `json:"encryption_default,omitempty"`
	CacheMaxAgeSeconds *int   `json:"cache_max_age_seconds,omitempty"`
}

type UpdateBucketRequest struct {
	Policy             *string `json:"policy,omitempty"`
	EncryptionDefault  *string `json:"encryption_default,omitempty"`
	CacheMaxAgeSeconds *int    `json:"cache_max_age_seconds,omitempty"`
}
