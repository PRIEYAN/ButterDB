package access

import "fmt"

const (
	AccessKeyIDPrefix = "BDBK"
	AccessKeyIDLength = 20
)

type AccessKeyID string

func ParseAccessKeyID(raw string) (AccessKeyID, error) {
	id := AccessKeyID(raw)
	if err := id.Validate(); err != nil {
		return "", err
	}
	return id, nil
}

func (id AccessKeyID) String() string {
	return string(id)
}

func (id AccessKeyID) Validate() error {
	text := string(id)
	if len(text) != AccessKeyIDLength || text[:len(AccessKeyIDPrefix)] != AccessKeyIDPrefix {
		return fmt.Errorf("access: key id: %w", ErrInvalidAccessKeyID)
	}
	for i := len(AccessKeyIDPrefix); i < len(text); i++ {
		if !isUpperAlphanumeric(text[i]) {
			return fmt.Errorf("access: key id: %w", ErrInvalidAccessKeyID)
		}
	}
	return nil
}

func isUpperAlphanumeric(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}
