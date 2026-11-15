package objectops

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/s3api/s3err"
)

const (
	headerACL               = "X-Amz-Acl"
	headerStorageClass      = "X-Amz-Storage-Class"
	headerTagging           = "X-Amz-Tagging"
	headerGrantPrefix       = "x-amz-grant-"
	headerSSECustomerPrefix = "x-amz-server-side-encryption-customer-"
	headerCopySourceSSEC    = "x-amz-copy-source-server-side-encryption-customer-"
	headerKMSKeyID          = "X-Amz-Server-Side-Encryption-Aws-Kms-Key-Id"
	headerEncryptionContext = "X-Amz-Server-Side-Encryption-Context"
	headerBucketKeyEnabled  = "X-Amz-Server-Side-Encryption-Bucket-Key-Enabled"
	headerWebsiteRedirect   = "X-Amz-Website-Redirect-Location"
	headerObjectLockPrefix  = "x-amz-object-lock-"
	standardStorageClass    = "STANDARD"
	versionIDNull           = "null"
	invalidVersionIDText    = "Invalid version id specified"
	aclNotAllowedText       = "The bucket does not allow ACLs"
)

func hasHeaderPrefix(header http.Header, prefix string) bool {
	for name := range header {
		if strings.HasPrefix(strings.ToLower(name), prefix) {
			return true
		}
	}
	return false
}

func checkVersionID(query url.Values) error {
	if values, present := query["versionId"]; present && (len(values) != 1 || values[0] != versionIDNull) {
		return invalidArgument(invalidVersionIDText)
	}
	return nil
}

func checkReadFeatures(header http.Header, query url.Values) error {
	if hasHeaderPrefix(header, headerSSECustomerPrefix) {
		return notImplemented("")
	}
	return checkVersionID(query)
}

func checkWriteFeatures(header http.Header, query url.Values) error {
	if err := checkReadFeatures(header, query); err != nil {
		return err
	}
	if hasHeaderPrefix(header, headerCopySourceSSEC) || hasHeaderPrefix(header, headerObjectLockPrefix) {
		return notImplemented("")
	}
	if header.Get(headerKMSKeyID) != "" || header.Get(headerEncryptionContext) != "" || header.Get(headerWebsiteRedirect) != "" {
		return notImplemented("")
	}
	if strings.EqualFold(strings.TrimSpace(header.Get(headerBucketKeyEnabled)), "true") {
		return notImplemented("")
	}
	if strings.TrimSpace(header.Get(headerTagging)) != "" {
		return notImplemented("")
	}
	if hasHeaderPrefix(header, headerGrantPrefix) {
		return s3err.New(s3err.CodeAccessControlListNotSupported, "")
	}
	return checkStorageClass(header)
}

func checkStorageClass(header http.Header) error {
	class := header.Get(headerStorageClass)
	if class == "" || class == standardStorageClass {
		return nil
	}
	return s3err.New(s3err.CodeInvalidStorageClass, "")
}

func checkCannedACL(header http.Header, info bucket.Info) error {
	switch canned := header.Get(headerACL); canned {
	case "", "private", "bucket-owner-full-control", "bucket-owner-read":
		return nil
	case "public-read":
		if info.Policy == bucket.PolicyPublicRead {
			return nil
		}
		return s3err.New(s3err.CodeAccessControlListNotSupported, "")
	}
	return s3err.New(s3err.CodeAccessControlListNotSupported, aclNotAllowedText)
}
