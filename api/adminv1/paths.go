package adminv1

const (
	BasePath             = "/admin/v1"
	HeaderRequestID      = "X-Request-Id"
	HeaderErrorCode      = "X-Butterdb-Error-Code"
	HeaderEncryption     = "X-Butterdb-Encryption"
	HeaderContentSHA256  = "X-Butterdb-Content-Sha256"
	HeaderMetaPrefix     = "X-Butterdb-Meta-"
	HeaderEncrypted      = "X-Butterdb-Encrypted"
	HeaderVersionID      = "X-Butterdb-Version-Id"
	HeaderIdempotencyKey = "Idempotency-Key"
)

const (
	HeaderAuthorization = "Authorization"
	BearerPrefix        = "Bearer "
	UnlinkConfirmation  = "unlink"
)

const (
	ParamDeviceID    = "device_id"
	ParamBucket      = "bucket"
	ParamAccessKeyID = "access_key_id"
)

const (
	QueryKey            = "key"
	QueryConfirmLast    = "confirm_last"
	QueryIncludeRevoked = "include_revoked"
	QueryPrefix         = "prefix"
	QueryDelimiter      = "delimiter"
	QueryCursor         = "cursor"
	QueryLimit          = "limit"
	QueryAction         = "action"
	QuerySince          = "since"
	QueryUntil          = "until"
)

const (
	PathServerInfo            = BasePath + "/server-info"
	PathSetupClaim            = BasePath + "/setup/claim"
	PathPairingRequest        = BasePath + "/pairing/request"
	PathPairingComplete       = BasePath + "/pairing/complete"
	PathDevices               = BasePath + "/devices"
	PathDevice                = BasePath + "/devices/{" + ParamDeviceID + "}"
	PathDeviceSelfRotate      = BasePath + "/devices/self/rotate"
	PathSetupTelegram         = BasePath + "/setup/telegram"
	PathSetupTelegramApp      = BasePath + "/setup/telegram/app"
	PathSetupTelegramPhone    = BasePath + "/setup/telegram/phone"
	PathSetupTelegramCode     = BasePath + "/setup/telegram/code"
	PathSetupTelegramPassword = BasePath + "/setup/telegram/password"
	PathSetupTelegramCancel   = BasePath + "/setup/telegram/cancel"
	PathSetupTelegramUnlink   = BasePath + "/setup/telegram/unlink"
	PathStatus                = BasePath + "/status"
	PathUsage                 = BasePath + "/usage"
	PathBuckets               = BasePath + "/buckets"
	PathBucket                = BasePath + "/buckets/{" + ParamBucket + "}"
	PathBucketObjects         = PathBucket + "/objects"
	PathBucketObjectsDelete   = PathBucket + "/objects/delete"
	PathBucketObject          = PathBucket + "/object"
	PathBucketObjectMeta      = PathBucket + "/object/meta"
	PathBucketPresign         = PathBucket + "/presign"
	PathAccessKeys            = BasePath + "/access-keys"
	PathAccessKey             = BasePath + "/access-keys/{" + ParamAccessKeyID + "}"
	PathAudit                 = BasePath + "/audit"
	PathReplica               = BasePath + "/replica"
	PathReplicaTelegramPhone  = BasePath + "/replica/telegram/phone"
	PathReplicaTelegramCode   = BasePath + "/replica/telegram/code"
	PathReplicaTelegramPass   = BasePath + "/replica/telegram/password"
	PathReplicaTelegramCancel = BasePath + "/replica/telegram/cancel"
	PathReplicaUnlink         = BasePath + "/replica/unlink"
)
