package bucket

import (
	"fmt"
	"strings"
)

const (
	MinNameLength = 3
	MaxNameLength = 63
)

func ParseName(raw string) (BucketName, error) {
	name := BucketName(raw)
	if err := name.Validate(); err != nil {
		return "", err
	}
	return name, nil
}

func (name BucketName) String() string {
	return string(name)
}

func (name BucketName) Validate() error {
	text := string(name)
	if len(text) < MinNameLength || len(text) > MaxNameLength {
		return invalidName("length must be 3 to 63 characters")
	}
	for i := 0; i < len(text); i++ {
		if !isNameByte(text[i]) {
			return invalidName("only a-z, 0-9, '.' and '-' are allowed")
		}
	}
	if !isLowerAlphanumeric(text[0]) || !isLowerAlphanumeric(text[len(text)-1]) {
		return invalidName("must start and end with a letter or digit")
	}
	if strings.Contains(text, "..") {
		return invalidName("must not contain '..'")
	}
	if isIPv4Address(text) {
		return invalidName("must not be an IP address")
	}
	if strings.HasPrefix(text, "xn--") {
		return invalidName("must not start with 'xn--'")
	}
	if strings.HasSuffix(text, "-s3alias") || strings.HasSuffix(text, "--ol-s3") {
		return invalidName("must not end with a reserved suffix")
	}
	return nil
}

func invalidName(reason string) error {
	return fmt.Errorf("bucket: name: %w: %s", ErrInvalidBucketName, reason)
}

func isNameByte(b byte) bool {
	return isLowerAlphanumeric(b) || b == '.' || b == '-'
}

func isLowerAlphanumeric(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9')
}

func isIPv4Address(text string) bool {
	groups := strings.Split(text, ".")
	if len(groups) != 4 {
		return false
	}
	for _, group := range groups {
		if !isDecimalGroup(group) {
			return false
		}
	}
	return true
}

func isDecimalGroup(group string) bool {
	if group == "" || len(group) > 3 {
		return false
	}
	for i := 0; i < len(group); i++ {
		if group[i] < '0' || group[i] > '9' {
			return false
		}
	}
	return true
}
