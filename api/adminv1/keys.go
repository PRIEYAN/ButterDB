package adminv1

import (
	"time"
)

type Scope struct {
	Buckets []string `json:"buckets"`
	Actions []string `json:"actions"`
}

type AccessKey struct {
	AccessKeyID string     `json:"access_key_id"`
	Name        string     `json:"name"`
	CreatedAt   time.Time  `json:"created_at"`
	LastUsedAt  *time.Time `json:"last_used_at"`
	Status      string     `json:"status"`
	RevokedAt   *time.Time `json:"revoked_at"`
	Scope       Scope      `json:"scope"`
}

type AccessKeyList struct {
	AccessKeys []AccessKey `json:"access_keys"`
}

type CreateAccessKeyRequest struct {
	Name  string `json:"name"`
	Scope Scope  `json:"scope"`
}

type UpdateAccessKeyRequest struct {
	Name  *string `json:"name,omitempty"`
	Scope *Scope  `json:"scope,omitempty"`
}

type AccessKeyCreated struct {
	AccessKey       AccessKey `json:"access_key"`
	SecretAccessKey string    `json:"secret_access_key"`
	S3Endpoint      string    `json:"s3_endpoint"`
	S3Region        string    `json:"s3_region"`
}
