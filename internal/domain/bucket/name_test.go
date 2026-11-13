package bucket

import (
	"errors"
	"strings"
	"testing"
)

func TestBucketNameRules(t *testing.T) {
	tests := []struct {
		name   string
		bucket string
		valid  bool
	}{
		{name: "three characters", bucket: "abc", valid: true},
		{name: "sixty three characters", bucket: strings.Repeat("a", 63), valid: true},
		{name: "digits only", bucket: "123", valid: true},
		{name: "hyphens inside", bucket: "my-bucket-1", valid: true},
		{name: "dots inside", bucket: "my.bucket.name", valid: true},
		{name: "starts with digit", bucket: "1bucket", valid: true},
		{name: "ends with digit", bucket: "bucket1", valid: true},
		{name: "not quite an address", bucket: "192.168.1", valid: true},
		{name: "five groups", bucket: "1.2.3.4.5", valid: true},
		{name: "address with letters", bucket: "192.168.1.a", valid: true},
		{name: "xn prefix mid name", bucket: "axn--b", valid: true},
		{name: "empty", bucket: "", valid: false},
		{name: "one character", bucket: "a", valid: false},
		{name: "two characters", bucket: "ab", valid: false},
		{name: "sixty four characters", bucket: strings.Repeat("a", 64), valid: false},
		{name: "uppercase", bucket: "MyBucket", valid: false},
		{name: "underscore", bucket: "my_bucket", valid: false},
		{name: "space", bucket: "my bucket", valid: false},
		{name: "slash", bucket: "my/bucket", valid: false},
		{name: "non ascii", bucket: "bücket", valid: false},
		{name: "starts with hyphen", bucket: "-bucket", valid: false},
		{name: "ends with hyphen", bucket: "bucket-", valid: false},
		{name: "starts with dot", bucket: ".bucket", valid: false},
		{name: "ends with dot", bucket: "bucket.", valid: false},
		{name: "double dot", bucket: "my..bucket", valid: false},
		{name: "ip address", bucket: "192.168.5.4", valid: false},
		{name: "ip address zeros", bucket: "0.0.0.0", valid: false},
		{name: "xn prefix", bucket: "xn--bucket", valid: false},
		{name: "s3alias suffix", bucket: "bucket-s3alias", valid: false},
		{name: "ol s3 suffix", bucket: "bucket--ol-s3", valid: false},
		{name: "nul byte", bucket: "buck\x00et", valid: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			name, err := ParseName(test.bucket)
			if test.valid {
				if err != nil {
					t.Fatalf("ParseName(%q) error = %v", test.bucket, err)
				}
				if name.String() != test.bucket {
					t.Fatalf("ParseName(%q) = %q", test.bucket, name)
				}
				return
			}
			if !errors.Is(err, ErrInvalidBucketName) {
				t.Fatalf("ParseName(%q) error = %v, want ErrInvalidBucketName", test.bucket, err)
			}
			if name != "" {
				t.Fatalf("ParseName(%q) = %q on error", test.bucket, name)
			}
		})
	}
}
