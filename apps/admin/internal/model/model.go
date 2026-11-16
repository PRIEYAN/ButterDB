package model

import (
	"errors"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
)

type Route string

const (
	RouteConnect   Route = "connect"
	RouteTelegram  Route = "telegram"
	RouteDashboard Route = "dashboard"
	RouteBuckets   Route = "buckets"
	RouteKeys      Route = "keys"
	RouteObjects   Route = "objects"
	RouteReplica   Route = "replica"
	RouteSettings  Route = "settings"
)

type Params map[string]string

const (
	ParamBucket = "bucket"
	ParamPrefix = "prefix"
	ParamStep   = "step"
)

type TokenStorage string

const (
	TokenKeychain TokenStorage = "keychain"
	TokenFile     TokenStorage = "file"
	TokenMemory   TokenStorage = "memory"
)

type Profile struct {
	ServerID      string        `json:"server_id"`
	Label         string        `json:"label"`
	BaseURL       string        `json:"base_url"`
	Trust         adminv1.Trust `json:"trust"`
	DeviceID      string        `json:"device_id"`
	DeviceName    string        `json:"device_name"`
	TokenStorage  TokenStorage  `json:"token_storage"`
	TokenIssuedAt time.Time     `json:"token_issued_at"`
	AddedAt       time.Time     `json:"added_at"`
	LastUsedAt    time.Time     `json:"last_used_at"`
}

var (
	ErrTokenNotFound       = errors.New("device token not found")
	ErrKeychainUnavailable = errors.New("os keychain unavailable")
	ErrProfileNotFound     = errors.New("server profile not found")
)

type TokenStore interface {
	Get(serverID string) (string, error)
	Set(serverID, token string) error
	Delete(serverID string) error
	Kind() TokenStorage
}

type ProfileStore interface {
	List() ([]Profile, error)
	Get(serverID string) (Profile, error)
	Put(p Profile) error
	Delete(serverID string) error
	LastUsed() (string, error)
	SetLastUsed(serverID string) error
}

type Clipboard interface {
	SetText(text string)
	Text() string
}
