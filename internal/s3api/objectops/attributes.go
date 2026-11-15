package objectops

import (
	"errors"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/limits"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/s3api/s3err"
)

const (
	userMetaHeaderPrefix = "x-amz-meta-"
	headerEncryption     = "X-Amz-Server-Side-Encryption"
	awsChunkedEncoding   = "aws-chunked"
	encryptionAES256     = "AES256"
	utf8Charset          = "UTF-8"
)

type attributes struct {
	contentType string
	userMeta    map[string]string
	systemMeta  map[string]string
}

type systemHeader struct {
	header string
	name   string
}

func systemHeaders() []systemHeader {
	return []systemHeader{
		{"Cache-Control", "cache-control"},
		{"Content-Disposition", "content-disposition"},
		{"Content-Encoding", "content-encoding"},
		{"Content-Language", "content-language"},
		{"Expires", "expires"},
	}
}

func parseAttributes(header http.Header) (attributes, error) {
	contentType := header.Get("Content-Type")
	if len(contentType) > limits.MaxContentTypeBytes {
		return attributes{}, invalidArgument("Header Content-Type exceeds 1024 bytes")
	}
	system, err := parseSystemMeta(header)
	if err != nil {
		return attributes{}, err
	}
	user, err := parseUserMeta(header)
	if err != nil {
		return attributes{}, err
	}
	return attributes{contentType: contentType, userMeta: user, systemMeta: system}, nil
}

func parseSystemMeta(header http.Header) (map[string]string, error) {
	system := map[string]string{}
	for _, entry := range systemHeaders() {
		value := strings.Join(header.Values(entry.header), ", ")
		if entry.name == "content-encoding" {
			value = withoutAWSChunked(value)
		}
		if value == "" {
			continue
		}
		if len(value) > limits.MaxSystemHeaderBytes {
			return nil, invalidArgument("Header " + entry.header + " exceeds 1024 bytes")
		}
		if !validMetaValue(value) {
			return nil, invalidArgument("Header " + entry.header + " contains an invalid character")
		}
		system[entry.name] = value
	}
	return system, nil
}

func withoutAWSChunked(value string) string {
	kept := make([]string, 0, 2)
	for _, token := range strings.Split(value, ",") {
		token = strings.TrimSpace(token)
		if token == "" || strings.EqualFold(token, awsChunkedEncoding) {
			continue
		}
		kept = append(kept, token)
	}
	return strings.Join(kept, ", ")
}

func parseUserMeta(header http.Header) (map[string]string, error) {
	user := map[string]string{}
	decoder := new(mime.WordDecoder)
	for rawName, values := range header {
		name, ok := strings.CutPrefix(strings.ToLower(rawName), userMetaHeaderPrefix)
		if !ok {
			continue
		}
		if name == "" {
			return nil, invalidArgument("Metadata name is empty")
		}
		decoded := make([]string, 0, len(values))
		for _, value := range values {
			text, err := decoder.DecodeHeader(value)
			if err != nil {
				return nil, invalidArgument("Metadata value for " + name + " is not valid")
			}
			decoded = append(decoded, text)
		}
		joined := strings.Join(decoded, ",")
		if !validMetaValue(joined) {
			return nil, invalidArgument("Metadata value for " + name + " contains an invalid character")
		}
		user[name] = joined
	}
	return user, checkUserMeta(user)
}

func checkUserMeta(user map[string]string) error {
	err := object.ValidateUserMeta(user)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, object.ErrMetadataTooLarge):
		e := s3err.New(s3err.CodeMetadataTooLarge, "")
		e.Extra = []s3err.Field{
			{Name: "Size", Value: strconv.Itoa(object.UserMetaBytes(user))},
			{Name: "MaxSizeAllowed", Value: strconv.Itoa(object.MaxUserMetadataBytes)},
		}
		return e
	}
	return invalidArgument("User metadata is not valid")
}

func validMetaValue(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c < 0x20 && c != '\t') || c == 0x7f {
			return false
		}
	}
	return true
}

func requestedEncryption(header http.Header) (string, error) {
	switch value := header.Get(headerEncryption); value {
	case "":
		return "", nil
	case encryptionAES256:
		return bucket.EncryptionTDE1, nil
	default:
		return "", invalidArgument("Server Side Encryption with " + value + " is not supported; use AES256")
	}
}

func resolveEncrypted(requested string, info bucket.Info) bool {
	if requested == "" {
		return info.DefaultEncryption == bucket.EncryptionTDE1
	}
	return requested == bucket.EncryptionTDE1
}

func encodeMetaForResponse(value string) string {
	for i := 0; i < len(value); i++ {
		if value[i] >= utf8.RuneSelf {
			return mime.BEncoding.Encode(utf8Charset, value)
		}
	}
	return value
}
