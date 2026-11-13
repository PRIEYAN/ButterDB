package bucket

type BucketName string

type Info struct {
	Name              BucketName
	ChannelID         int64
	Policy            string
	DefaultEncryption string
	CacheMaxAge       int64
	CreatedDate       int64
	ObjectCount       int64
	StoredBytes       int64
	PlaintextBytes    int64
}

type Spec struct {
	Name              BucketName
	Policy            string
	DefaultEncryption string
	CacheMaxAge       int64
}

type Patch struct {
	Policy            *string
	DefaultEncryption *string
	CacheMaxAge       *int64
}
