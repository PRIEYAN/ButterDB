package jobrepo

import (
	"context"
	"database/sql"
	"errors"
)

type Database interface {
	Read(ctx context.Context, fn func(tx *sql.Tx) error) error
	Write(ctx context.Context, fn func(tx *sql.Tx) error) error
}

var ErrJobNotFound = errors.New("jobrepo: job not found")
var ErrDuplicateJob = errors.New("jobrepo: job already exists")
var ErrStaleTransition = errors.New("jobrepo: stale transition")
var ErrInvalidTransition = errors.New("jobrepo: transition not allowed")
var ErrCorruptRow = errors.New("jobrepo: corrupt row")

type UploadKind string

const (
	KindPut       UploadKind = "put"
	KindCopy      UploadKind = "copy"
	KindMultipart UploadKind = "multipart"
)

type Origin string

const (
	OriginS3    Origin = "s3"
	OriginAdmin Origin = "admin"
)

type UploadState string

const (
	UploadPrepared      UploadState = "prepared"
	UploadBodySending   UploadState = "body_sending"
	UploadUncertainBody UploadState = "uncertain_body"
	UploadBodySent      UploadState = "body_sent"
	UploadPutAttempted  UploadState = "put_attempted"
	UploadUncertainPut  UploadState = "uncertain_put"
	UploadCommitted     UploadState = "committed"
	UploadAborted       UploadState = "aborted"
)

type UploadJob struct {
	JobID             string
	Kind              UploadKind
	Origin            Origin
	Scope             string
	Writer            string
	BucketChannelID   int64
	BucketName        string
	ObjectKey         string
	MultipartUploadID string
	State             UploadState
	BasePutMsgID      int64
	IfMatch           string
	IfNoneMatch       bool
	SpoolPath         string
	SealedPath        string
	PlaintextSize     int64
	StoredSize        int64
	ETag              string
	SHA256            string
	ContentType       string
	UserMetaJSON      string
	SystemMetaJSON    string
	Encrypted         bool
	EncryptionVersion int
	KeyID             string
	FileSalt          []byte
	NoncePrefix       []byte
	BodyMsgID         int64
	PutHeader         string
	PutMsgID          int64
	ErrorCode         string
	Attempts          int
	CreatedAt         int64
	UpdatedAt         int64
}

type UploadPatch struct {
	SealedPath        *string
	BodyMsgID         *int64
	PutHeader         *string
	PutMsgID          *int64
	ErrorCode         *string
	IncrementAttempts bool
}

type DeleteState string

const (
	DeletePrepared     DeleteState = "prepared"
	DeleteDelAttempted DeleteState = "del_attempted"
	DeleteUncertainDel DeleteState = "uncertain_del"
	DeleteCommitted    DeleteState = "committed"
	DeleteAborted      DeleteState = "aborted"
)

type DeleteJob struct {
	JobID           string
	Origin          Origin
	Scope           string
	Writer          string
	RequestID       string
	BucketChannelID int64
	BucketName      string
	ObjectKey       string
	State           DeleteState
	TargetPutMsgID  int64
	DelHeader       string
	DelMsgID        int64
	ErrorCode       string
	Attempts        int
	CreatedAt       int64
	UpdatedAt       int64
}

type DeletePatch struct {
	DelHeader         *string
	DelMsgID          *int64
	ErrorCode         *string
	IncrementAttempts bool
}

type IdempotencyRecord struct {
	Scope           string
	IdempotencyKey  string
	BucketChannelID int64
	ObjectKey       string
	BodySHA256      string
	JobID           string
	CreatedAt       int64
	ExpiresAt       int64
}

type MultipartState string

const (
	MultipartOpen       MultipartState = "open"
	MultipartCommitting MultipartState = "committing"
	MultipartCompleted  MultipartState = "completed"
	MultipartAborted    MultipartState = "aborted"
)

type MultipartUpload struct {
	UploadID             string
	BucketChannelID      int64
	BucketName           string
	ObjectKey            string
	InitiatorAccessKeyID string
	InitiatedAt          int64
	LastActivityAt       int64
	ContentType          string
	UserMetaJSON         string
	SystemMetaJSON       string
	Encrypted            bool
	ChecksumAlgorithm    string
	ChecksumType         string
	State                MultipartState
	JobID                string
	ETag                 string
	PutMsgID             int64
	PartListSHA256       string
	CompletedAt          int64
}

type MultipartPart struct {
	UploadID      string
	PartNumber    int
	Size          int64
	MD5           string
	ChecksumValue string
	StagedAt      int64
}

type MultipartPatch struct {
	JobID          *string
	ETag           *string
	PutMsgID       *int64
	PartListSHA256 *string
	CompletedAt    *int64
	LastActivityAt *int64
}
