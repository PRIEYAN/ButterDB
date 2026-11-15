package multipart

import (
	"errors"
	"fmt"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/limits"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/s3api/s3err"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

const (
	userMetaHeaderPrefix  = "x-amz-meta-"
	awsChunkedCoding      = "aws-chunked"
	privateACL            = "private"
	publicReadACL         = "public-read"
	aclNotAllowedText     = "The bucket does not allow ACLs"
	checksumAlgorithmName = "X-Amz-Checksum-Algorithm"
	checksumTypeName      = "X-Amz-Checksum-Type"
)

type uploadAttributes struct {
	contentType       string
	userMeta          map[string]string
	systemMeta        map[string]string
	encrypted         bool
	checksumAlgorithm string
	checksumType      string
}

func checksumAlgorithms() []string {
	return []string{"CRC32", "CRC32C", "CRC64NVME", "SHA1", "SHA256"}
}

func systemHeaders() []string {
	return []string{"Cache-Control", "Content-Disposition", "Content-Encoding", "Content-Language", "Expires"}
}

func parseAttributes(header http.Header, info bucket.Info) (uploadAttributes, error) {
	if err := rejectUnsupportedFeatures(header, info); err != nil {
		return uploadAttributes{}, err
	}
	var attrs uploadAttributes
	var err error
	if attrs.contentType, err = parseContentType(header); err != nil {
		return uploadAttributes{}, err
	}
	if attrs.systemMeta, err = parseSystemMeta(header); err != nil {
		return uploadAttributes{}, err
	}
	if attrs.userMeta, err = parseUserMeta(header); err != nil {
		return uploadAttributes{}, err
	}
	if attrs.encrypted, err = parseEncryption(header, info); err != nil {
		return uploadAttributes{}, err
	}
	if attrs.checksumAlgorithm, attrs.checksumType, err = parseChecksumRequest(header); err != nil {
		return uploadAttributes{}, err
	}
	return attrs, nil
}

func rejectUnsupportedFeatures(header http.Header, info bucket.Info) error {
	for name, values := range header {
		if err := unsupportedHeader(strings.ToLower(name), values); err != nil {
			return err
		}
	}
	if class := header.Get("X-Amz-Storage-Class"); class != "" && class != storageClassStandard {
		return s3err.New(s3err.CodeInvalidStorageClass, "")
	}
	return checkCannedACL(header.Get("X-Amz-Acl"), info)
}

func unsupportedHeader(name string, values []string) error {
	switch {
	case strings.HasPrefix(name, "x-amz-grant-"):
		return s3err.New(s3err.CodeAccessControlListNotSupported, "")
	case strings.HasPrefix(name, "x-amz-server-side-encryption-customer-"),
		strings.HasPrefix(name, "x-amz-object-lock-"),
		name == "x-amz-server-side-encryption-aws-kms-key-id",
		name == "x-amz-server-side-encryption-context",
		name == "x-amz-website-redirect-location":
		return s3err.New(s3err.CodeNotImplemented, "")
	case name == "x-amz-server-side-encryption-bucket-key-enabled" && slices.Contains(values, "true"):
		return s3err.New(s3err.CodeNotImplemented, "")
	case name == "x-amz-tagging" && strings.Join(values, "") != "":
		return s3err.New(s3err.CodeNotImplemented, "")
	}
	return nil
}

func checkCannedACL(value string, info bucket.Info) error {
	switch value {
	case "", privateACL, "bucket-owner-full-control", "bucket-owner-read":
		return nil
	case publicReadACL:
		if info.Policy == bucket.PolicyPublicRead {
			return nil
		}
	}
	return s3err.New(s3err.CodeAccessControlListNotSupported, aclNotAllowedText)
}

func parseContentType(header http.Header) (string, error) {
	contentType := header.Get("Content-Type")
	if contentType == "" {
		return wire.DefaultContentType, nil
	}
	if len(contentType) > limits.MaxContentTypeBytes {
		return "", s3err.New(s3err.CodeInvalidArgument, "Content-Type is too long")
	}
	return contentType, nil
}

func parseSystemMeta(header http.Header) (map[string]string, error) {
	meta := make(map[string]string)
	for _, name := range systemHeaders() {
		value := strings.Join(header.Values(name), ",")
		if name == "Content-Encoding" {
			value = withoutAWSChunked(value)
		}
		if value == "" {
			continue
		}
		if len(value) > limits.MaxSystemHeaderBytes {
			return nil, s3err.New(s3err.CodeInvalidArgument, name+" is too long")
		}
		meta[strings.ToLower(name)] = value
	}
	return meta, nil
}

func withoutAWSChunked(value string) string {
	var kept []string
	for _, coding := range strings.Split(value, ",") {
		coding = strings.TrimSpace(coding)
		if coding != "" && !strings.EqualFold(coding, awsChunkedCoding) {
			kept = append(kept, coding)
		}
	}
	return strings.Join(kept, ",")
}

func parseUserMeta(header http.Header) (map[string]string, error) {
	meta := make(map[string]string)
	decoder := new(mime.WordDecoder)
	for name, values := range header {
		lowered := strings.ToLower(name)
		short, ok := strings.CutPrefix(lowered, userMetaHeaderPrefix)
		if !ok {
			continue
		}
		decoded, err := decoder.DecodeHeader(strings.Join(values, ","))
		if err != nil {
			return nil, s3err.New(s3err.CodeInvalidArgument, "user metadata is not valid RFC 2047 text")
		}
		meta[object.NormalizeUserMetaName(short)] = decoded
	}
	if err := object.ValidateUserMeta(meta); err != nil {
		return nil, userMetaError(meta, err)
	}
	return meta, nil
}

func userMetaError(meta map[string]string, err error) error {
	if errors.Is(err, object.ErrMetadataTooLarge) {
		return metadataTooLarge(object.UserMetaBytes(meta), limits.MaxUserMetadataBytes, "")
	}
	return s3err.New(s3err.CodeInvalidArgument, "")
}

func metadataTooLarge(size, maxAllowed int, message string) s3err.APIError {
	api := s3err.New(s3err.CodeMetadataTooLarge, message)
	api.Extra = []s3err.Field{
		{Name: "Size", Value: strconv.Itoa(size)},
		{Name: "MaxSizeAllowed", Value: strconv.Itoa(maxAllowed)},
	}
	return api
}

func parseEncryption(header http.Header, info bucket.Info) (bool, error) {
	switch value := header.Get(serverSideEncryption); value {
	case "":
		return info.DefaultEncryption == bucket.EncryptionTDE1, nil
	case serverSideEncryptionAES:
		return true, nil
	default:
		return false, s3err.New(s3err.CodeInvalidArgument, fmt.Sprintf("Server Side Encryption with %s is not supported; use AES256", value))
	}
}

func parseChecksumRequest(header http.Header) (algorithm, checksumType string, err error) {
	algorithm = header.Get(checksumAlgorithmName)
	if algorithm != "" && !slices.Contains(checksumAlgorithms(), algorithm) {
		return "", "", s3err.New(s3err.CodeInvalidArgument, "Checksum algorithm provided is unsupported.")
	}
	checksumType = header.Get(checksumTypeName)
	if checksumType != "" && checksumType != "COMPOSITE" && checksumType != "FULL_OBJECT" {
		return "", "", s3err.New(s3err.CodeInvalidArgument, "The checksum type that you provided is not valid.")
	}
	return algorithm, checksumType, nil
}

func checkPutRecord(key string, attrs uploadAttributes, writer string) error {
	err := wire.FitsPut(key, attrs.contentType, attrs.userMeta, attrs.systemMeta, writer)
	if err == nil {
		return nil
	}
	if !errors.Is(err, wire.ErrWireTooLong) {
		return s3err.New(s3err.CodeInvalidArgument, "")
	}
	if wire.FitsPut(key, attrs.contentType, nil, nil, writer) != nil {
		return keyTooLong(len(key))
	}
	length, lengthErr := wire.PutRecordLen(key, attrs.contentType, attrs.userMeta, attrs.systemMeta, writer)
	if lengthErr != nil {
		return s3err.New(s3err.CodeInvalidArgument, "")
	}
	message := fmt.Sprintf("The encoded object record is %d characters; the limit is %d", length, limits.MaxPutOpChars)
	return metadataTooLarge(length, limits.MaxPutOpChars, message)
}

func keyTooLong(size int) s3err.APIError {
	api := s3err.New(s3err.CodeKeyTooLongError, "")
	api.Extra = []s3err.Field{
		{Name: "Size", Value: strconv.Itoa(size)},
		{Name: "MaxSizeAllowed", Value: strconv.Itoa(limits.MaxKeyBytes)},
	}
	return api
}
