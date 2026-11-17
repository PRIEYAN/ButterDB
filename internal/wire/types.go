package wire

import "errors"

type OpType string

const (
	OpBody         OpType = "body"
	OpPut          OpType = "put"
	OpDel          OpType = "del"
	OpRegistry     OpType = "reg"
	OpBucketCreate OpType = "bcreate"
	OpBucketConfig OpType = "bcfg"
	OpBucketDelete OpType = "bdel"
	OpEncConfig    OpType = "encfg"
	OpKeyEscrow    OpType = "kesc"
)

const (
	Prefix              = "TDX1"
	FormatVersion       = 2
	MaxUnixTimestamp    = int64(253402300799)
	MaxTextChars        = 4096
	MaxCaptionChars     = 1024
	MaxMsgID            = int64(2147483647)
	MaxKeyBytes         = 1024
	MaxEncodedKeyChars  = 3 * MaxKeyBytes
	MaxContentTypeBytes = 1024
	MaxUserMetaBytes    = 2048
	MaxSysHeaderBytes   = 1024
	MaxPutFixedChars    = 512
	MaxStoredBytes      = int64(1900 * 1024 * 1024)
	MaxCacheMaxAge      = int64(31536000)
	DefaultContentType  = "application/octet-stream"
	EmptyMD5            = "d41d8cd98f00b204e9800998ecf8427e"
	EmptySHA256         = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

var (
	ErrWireMissingHeader = errors.New("wire: header missing")
	ErrWireBadVersion    = errors.New("wire: unknown version")
	ErrWireMalformed     = errors.New("wire: malformed header")
	ErrWireUnknownOp     = errors.New("wire: unknown op type")
	ErrWireTooLong       = errors.New("wire: encoded op exceeds the telegram length limit")
)

type Op struct {
	Type              OpType
	JobID             string
	Key               string
	BodyMsgID         int64
	StoredSize        int64
	PlaintextSize     int64
	Encrypted         bool
	EncryptionVersion int
	KeyID             string
	ETag              string
	SHA256            string
	ContentType       string
	UserMeta          map[string]string
	SystemMeta        map[string]string
	Writer            string
	Timestamp         int64
	PutVersion        int64
	Bucket            string
	BucketChannelID   int64
	Policy            string
	DefaultEncryption string
	CacheMaxAge       int64
	WrappedKey        []byte
	KeyCheck          []byte
	KEKFingerprint    string
	ConfigVersion     int
	DeploymentID      string
	FormatVersion     int
	MinReaderVersion  int
	PrimaryUserID     int64
	EscrowKDF         string
	EscrowSalt        []byte
	EscrowTime        int
	EscrowMemoryKiB   int
	EscrowParallel    int
}
