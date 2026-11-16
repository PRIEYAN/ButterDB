package vm

import "strings"

const redacted = "[secret withheld]"

type Secret struct {
	AccessKeyID     string
	SecretAccessKey string
	S3Endpoint      string
	Region          string
}

func (s Secret) EnvBlock() string {
	lines := []string{
		"AWS_ACCESS_KEY_ID=" + s.AccessKeyID,
		"AWS_SECRET_ACCESS_KEY=" + s.SecretAccessKey,
	}
	if s.S3Endpoint != "" {
		lines = append(lines, "AWS_ENDPOINT_URL_S3="+s.S3Endpoint)
	}
	if s.Region != "" {
		lines = append(lines, "AWS_REGION="+s.Region)
	}
	return strings.Join(lines, "\n")
}

func (s Secret) String() string { return redacted }

func (s Secret) GoString() string { return redacted }
