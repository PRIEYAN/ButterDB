package vm

import (
	"strconv"
	"strings"
)

const (
	maxAPIID       = 2147483647
	apiHashLength  = 32
	minPhoneDigits = 7
	maxPhoneDigits = 15
)

const (
	reasonAPIID    = "api_id is a number from 1 to 2147483647"
	reasonAPIHash  = "api_hash is 32 hexadecimal characters"
	reasonPhone    = "enter the number with its country code, like +447700900134"
	reasonCode     = "enter the login code"
	reasonPassword = "enter the two-step password"
)

func parseAPIID(text string) (int, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, false
	}
	for _, r := range text {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(text)
	if err != nil || n < 1 || n > maxAPIID {
		return 0, false
	}
	return n, true
}

func normaliseAPIHash(text string) (string, bool) {
	hash := strings.ToLower(strings.TrimSpace(text))
	if len(hash) != apiHashLength {
		return "", false
	}
	for _, r := range hash {
		if !isLowerHex(r) {
			return "", false
		}
	}
	return hash, true
}

func isLowerHex(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')
}

func normalisePhone(text string) (string, bool) {
	var digits strings.Builder
	for _, r := range text {
		switch {
		case r >= '0' && r <= '9':
			digits.WriteRune(r)
		case r == '+' && digits.Len() == 0, r == ' ', r == '-', r == '(', r == ')':
		default:
			return "", false
		}
	}
	number := digits.String()
	if len(number) < minPhoneDigits || len(number) > maxPhoneDigits || number[0] == '0' {
		return "", false
	}
	return "+" + number, true
}
