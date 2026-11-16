package handlers

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/internal/adminapi/setup"
	"github.com/PRIEYAN/ButterDB/internal/domain/access"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/storage/adminrepo"
)

type BucketService interface {
	List(ctx context.Context) ([]bucket.Info, error)
	Create(ctx context.Context, spec bucket.Spec) (bucket.Info, error)
	Get(ctx context.Context, name bucket.BucketName) (bucket.Info, error)
	Update(ctx context.Context, name bucket.BucketName, patch bucket.Patch) (bucket.Info, error)
	Delete(ctx context.Context, name bucket.BucketName) error
}

type KeyService interface {
	List(ctx context.Context, includeRevoked bool) ([]access.KeyInfo, error)
	Create(ctx context.Context, name string, scope access.Scope) (access.KeyInfo, string, error)
	Get(ctx context.Context, id access.AccessKeyID) (access.KeyInfo, error)
	Update(ctx context.Context, id access.AccessKeyID, name *string, scope *access.Scope) (access.KeyInfo, error)
	Revoke(ctx context.Context, id access.AccessKeyID) (access.KeyInfo, error)
}

type ObjectLister interface {
	List(ctx context.Context, b bucket.BucketName, q object.ListQuery) (object.ListPage, error)
	Meta(ctx context.Context, b bucket.BucketName, k object.Key) (object.ObjectMeta, error)
}

type ObjectWriter interface {
	Put(ctx context.Context, req object.PutRequest, body io.Reader) (object.ObjectMeta, error)
	Delete(ctx context.Context, b bucket.BucketName, k object.Key) error
}

type ObjectServer interface {
	ServeObject(w http.ResponseWriter, r *http.Request, b bucket.BucketName, k object.Key) error
}

type Presigner interface {
	PresignGet(ctx context.Context, id access.AccessKeyID, b bucket.BucketName, k object.Key, ttl time.Duration) (string, time.Time, error)
}

type StatusSource interface {
	Status(ctx context.Context) (adminv1.Status, error)
	Usage(ctx context.Context) (adminv1.Usage, error)
}

type AuditRecord struct {
	At         time.Time
	Actor      adminv1.AuditActor
	Action     string
	Target     string
	Outcome    string
	ErrorCode  string
	RemoteAddr string
	RequestID  string
}

type AuditLog interface {
	Record(ctx context.Context, e AuditRecord) error
	Page(ctx context.Context, q adminv1.AuditQuery) (adminv1.AuditPage, error)
}

type SetupEndpoints interface {
	ServerInfo(ctx context.Context) (adminv1.ServerInfo, error)
	Claim(ctx context.Context, token string, device adminv1.DeviceInfo) (adminv1.ClaimResult, error)
	Relay() setup.LoginRelay
}

type PairingEndpoints interface {
	Request(ctx context.Context, delivery string, device adminv1.DeviceInfo) (time.Time, error)
	Complete(ctx context.Context, code string, device adminv1.DeviceInfo) (adminv1.DeviceTokenResult, error)
}

type DeviceService interface {
	List(ctx context.Context) ([]adminrepo.Device, error)
	Rename(ctx context.Context, deviceID string, name string) (adminrepo.Device, error)
	Revoke(ctx context.Context, deviceID string, confirmLast bool) error
	Rotate(ctx context.Context, deviceID string) (adminrepo.Device, string, error)
}

type DeviceResolver func(ctx context.Context) (adminrepo.Device, bool)

type Config struct {
	S3Endpoint     string
	S3Region       string
	AllowedHosts   []string
	TrustedProxies []string
}

type Dependencies struct {
	Buckets BucketService
	Keys    KeyService
	Lister  ObjectLister
	Writer  ObjectWriter
	Server  ObjectServer
	Presign Presigner
	Status  StatusSource
	Audit   AuditLog
	Setup   SetupEndpoints
	Pairing PairingEndpoints
	Devices DeviceService
	Device  DeviceResolver
	Auth    func(http.Handler) http.Handler
	Clock   clock.Clock
	Random  io.Reader
	Logger  *slog.Logger
	Config  Config
}
