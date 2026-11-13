package limits

import "time"

const (
	MaxStoredObjectBytes       = 1900 * 1024 * 1024
	MaxEncryptedPlaintextBytes = 1991808062

	MaxKeyBytes          = 1024
	MaxEncodedKeyChars   = 3 * MaxKeyBytes
	MaxUserMetadataBytes = 2048
	MaxSystemHeaderBytes = 1024
	MaxContentTypeBytes  = 1024

	MaxPutFixedChars = 512
	MaxPutOpChars    = 4096
	MaxCaptionChars  = 1024

	DeleteBatchSize      = 100
	MaxBuckets           = 100
	MaxDeleteObjectsKeys = 1000

	MinMultipartPartBytes = 5 * 1024 * 1024
	MaxMultipartParts     = 10000

	MaxCacheMaxAgeSeconds = 31536000

	OrphanBodyGrace     = 24 * time.Hour
	IdempotencyTTL      = 24 * time.Hour
	DefaultCloseTimeout = 30 * time.Second
)
