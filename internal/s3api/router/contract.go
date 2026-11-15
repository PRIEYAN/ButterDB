package router

type Operation string

const (
	OpListBuckets             Operation = "ListBuckets"
	OpCreateBucket            Operation = "CreateBucket"
	OpHeadBucket              Operation = "HeadBucket"
	OpDeleteBucket            Operation = "DeleteBucket"
	OpGetBucketLocation       Operation = "GetBucketLocation"
	OpListObjectsV2           Operation = "ListObjectsV2"
	OpListObjects             Operation = "ListObjects"
	OpPutObject               Operation = "PutObject"
	OpGetObject               Operation = "GetObject"
	OpHeadObject              Operation = "HeadObject"
	OpDeleteObject            Operation = "DeleteObject"
	OpDeleteObjects           Operation = "DeleteObjects"
	OpCopyObject              Operation = "CopyObject"
	OpCreateMultipartUpload   Operation = "CreateMultipartUpload"
	OpUploadPart              Operation = "UploadPart"
	OpUploadPartCopy          Operation = "UploadPartCopy"
	OpCompleteMultipartUpload Operation = "CompleteMultipartUpload"
	OpAbortMultipartUpload    Operation = "AbortMultipartUpload"
	OpListParts               Operation = "ListParts"
	OpListMultipartUploads    Operation = "ListMultipartUploads"
	OpGetBucketPolicy         Operation = "GetBucketPolicy"
	OpGetBucketPolicyStatus   Operation = "GetBucketPolicyStatus"
	OpGetBucketCors           Operation = "GetBucketCors"
	OpGetBucketAcl            Operation = "GetBucketAcl"
	OpPutBucketAcl            Operation = "PutBucketAcl"
	OpGetObjectAcl            Operation = "GetObjectAcl"
	OpPutObjectAcl            Operation = "PutObjectAcl"
	OpGetBucketVersioning     Operation = "GetBucketVersioning"
	OpGetBucketEncryption     Operation = "GetBucketEncryption"
	OpGetObjectTagging        Operation = "GetObjectTagging"
	OpPutObjectTagging        Operation = "PutObjectTagging"
	OpDeleteObjectTagging     Operation = "DeleteObjectTagging"
	OpGetBucketConfig         Operation = "GetBucketConfig"
	OpPreflight               Operation = "Preflight"
)

type Target struct {
	Bucket        string
	Key           string
	HasKey        bool
	VirtualHosted bool
}

type Route struct {
	Target      Target
	Operation   Operation
	Subresource string
}

type Addressing struct {
	BaseDomains  []string
	AllowedHosts []string
	LoopbackOnly bool
}
