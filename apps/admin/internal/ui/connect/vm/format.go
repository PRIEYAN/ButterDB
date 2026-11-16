package vm

import (
	"strings"
	"unicode"
)

const (
	pairingCodeLength = 10
	pairingCodeSplit  = 5
	fingerprintPrefix = "SHA256:"
	fingerprintGroup  = 4
	fingerprintLine   = 4
)

func FormatPairingCode(raw string) string {
	var b strings.Builder
	count := 0
	for _, r := range raw {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			continue
		}
		if r > unicode.MaxASCII || count == pairingCodeLength {
			continue
		}
		if count == pairingCodeSplit {
			b.WriteByte('-')
		}
		b.WriteRune(unicode.ToUpper(r))
		count++
	}
	return b.String()
}

func PairingCodeComplete(formatted string) bool {
	return len(strings.ReplaceAll(formatted, "-", "")) == pairingCodeLength
}

func FingerprintLines(pin string) string {
	text := strings.TrimSpace(pin)
	if len(text) >= len(fingerprintPrefix) && strings.EqualFold(text[:len(fingerprintPrefix)], fingerprintPrefix) {
		text = text[len(fingerprintPrefix):]
	}
	hex := strings.ToLower(strings.Join(strings.Fields(text), ""))
	var groups []string
	for i := 0; i < len(hex); i += fingerprintGroup {
		groups = append(groups, hex[i:min(i+fingerprintGroup, len(hex))])
	}
	var lines []string
	for i := 0; i < len(groups); i += fingerprintLine {
		lines = append(lines, strings.Join(groups[i:min(i+fingerprintLine, len(groups))], " "))
	}
	return strings.Join(lines, "\n")
}

func canonicalPin(pin string) string {
	text := strings.TrimSpace(pin)
	if len(text) >= len(fingerprintPrefix) && strings.EqualFold(text[:len(fingerprintPrefix)], fingerprintPrefix) {
		text = text[len(fingerprintPrefix):]
	}
	return strings.ToLower(strings.Join(strings.Fields(text), ""))
}
