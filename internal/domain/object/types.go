package object

type ObjectMeta struct {
	ChannelID         int64
	Key               string
	PutMsgID          int64
	BodyMsgID         int64
	JobID             string
	StoredSize        int64
	PlaintextSize     int64
	ETag              string
	SHA256            string
	ContentType       string
	UserMeta          map[string]string
	SystemMeta        map[string]string
	Encrypted         bool
	EncryptionVersion int
	KeyID             string
	Writer            string
	MsgDate           int64
}

type Key string

type PutRequest struct {
	Bucket         string
	Key            Key
	ContentLength  int64
	ContentType    string
	UserMeta       map[string]string
	SystemMeta     map[string]string
	Encryption     string
	AccessKeyID    string
	IdempotencyKey string
	IfMatch        string
	IfNoneMatchAny bool
}

type ListQuery struct {
	Prefix            string
	Delimiter         string
	StartAfter        string
	ContinuationToken string
	Limit             int
}

type ListPage struct {
	Objects        []ObjectMeta
	CommonPrefixes []string
	NextToken      string
}
