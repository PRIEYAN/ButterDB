package identity

import "strings"

const (
	maskedDigit    = "•"
	minPhoneDigits = 7
	maxPhoneDigits = 15
)

func NormalizePhone(raw string) (string, error) {
	phone := strings.NewReplacer(" ", "", "-", "", "(", "", ")", "").Replace(strings.TrimSpace(raw))
	digits, found := strings.CutPrefix(phone, "+")
	if !found || len(digits) < minPhoneDigits || len(digits) > maxPhoneDigits || digits[0] == '0' {
		return "", ErrPhoneInvalid
	}
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return "", ErrPhoneInvalid
		}
	}
	return phone, nil
}

func MaskPhone(phone string) string {
	digits := strings.TrimPrefix(phone, "+")
	if len(digits) < minPhoneDigits {
		return "+" + strings.Repeat(maskedDigit, len(digits))
	}
	code := callingCodeLength(digits)
	last := digits[len(digits)-2:]
	return "+" + digits[:code] + " " + strings.Repeat(maskedDigit, 3) + " " + strings.Repeat(maskedDigit, 2) + " " + last
}

func callingCodeLength(digits string) int {
	switch digits[0] {
	case '1', '7':
		return 1
	}
	if isTwoDigitCallingCode(digits[:2]) {
		return 2
	}
	return 3
}

func isTwoDigitCallingCode(code string) bool {
	switch code {
	case "20", "27", "30", "31", "32", "33", "34", "36", "39", "40", "41",
		"43", "44", "45", "46", "47", "48", "49",
		"51", "52", "53", "54", "55", "56", "57", "58",
		"60", "61", "62", "63", "64", "65", "66",
		"81", "82", "84", "86", "90", "91", "92", "93", "94", "95", "98":
		return true
	}
	return false
}
