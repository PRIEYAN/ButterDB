package logging

import (
	"log/slog"
	"strings"
)

const redactedText = "[redacted]"

type Secret string

type Phone string

func (Secret) LogValue() slog.Value {
	return slog.StringValue(redactedText)
}

func (Secret) String() string {
	return redactedText
}

func (Secret) GoString() string {
	return redactedText
}

func (p Phone) LogValue() slog.Value {
	return slog.StringValue(MaskPhone(string(p)))
}

func (p Phone) String() string {
	return MaskPhone(string(p))
}

func (p Phone) GoString() string {
	return MaskPhone(string(p))
}

const (
	minPhoneDigits   = 7
	visibleTailDigit = 3
)

func MaskPhone(phone string) string {
	digits := onlyDigits(phone)
	codeLength := countryCodeLength(digits)
	if len(digits) < minPhoneDigits || len(digits) <= codeLength+visibleTailDigit {
		return redactedText
	}
	hidden := len(digits) - codeLength - visibleTailDigit
	return "+" + digits[:codeLength] + strings.Repeat("*", hidden) + digits[len(digits)-visibleTailDigit:]
}

func onlyDigits(text string) string {
	var digits strings.Builder
	for _, r := range text {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		}
	}
	return digits.String()
}

func countryCodeLength(digits string) int {
	if len(digits) < 3 {
		return len(digits)
	}
	switch digits[0] {
	case '1', '7':
		return 1
	case '2':
		return lengthUnless(digits, "20", "27")
	case '3':
		return lengthUnless(digits, "30", "31", "32", "33", "34", "36", "39")
	case '4':
		return lengthUnless(digits, "40", "41", "43", "44", "45", "46", "47", "48", "49")
	case '5':
		return lengthUnless(digits, "51", "52", "53", "54", "55", "56", "57", "58")
	case '6':
		return lengthUnless(digits, "60", "61", "62", "63", "64", "65", "66")
	case '8':
		return lengthUnless(digits, "81", "82", "84", "86")
	case '9':
		return lengthUnless(digits, "90", "91", "92", "93", "94", "95", "98")
	}
	return 3
}

func lengthUnless(digits string, twoDigitCodes ...string) int {
	for _, code := range twoDigitCodes {
		if strings.HasPrefix(digits, code) {
			return 2
		}
	}
	return 3
}
