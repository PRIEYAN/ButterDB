package vm

import (
	"strings"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

const (
	MinNameLength = 3
	MaxNameLength = 63

	reasonNameLength  = "Use 3 to 63 characters."
	reasonNameChars   = "Use only lowercase letters, digits, hyphens and dots."
	reasonNameEnds    = "Start and end with a letter or a digit."
	reasonNameDots    = "Do not put two dots in a row."
	reasonNameAddress = "Do not use an IP address."
	reasonCacheRange  = "Use 0 to 31536000 seconds."
)

func CheckName(name string) string {
	switch {
	case len(name) < MinNameLength || len(name) > MaxNameLength:
		return reasonNameLength
	case !onlyNameCharacters(name):
		return reasonNameChars
	case !isLowerAlnum(name[0]) || !isLowerAlnum(name[len(name)-1]):
		return reasonNameEnds
	case strings.Contains(name, ".."):
		return reasonNameDots
	case looksLikeIPv4(name):
		return reasonNameAddress
	}
	return ""
}

func CheckCacheMaxAge(seconds int) string {
	if seconds < 0 || seconds > MaxCacheMaxAgeSeconds {
		return reasonCacheRange
	}
	return ""
}

func precheckNew(in NewBucket, now time.Time) *model.Problem {
	if reason := CheckName(in.Name); reason != "" {
		return localProblem(adminv1.CodeBucketNameInvalid, map[string]any{"reason": reason}, now)
	}
	return precheckCacheMaxAge(in.CacheMaxAgeSeconds, now)
}

func precheckCacheMaxAge(seconds int, now time.Time) *model.Problem {
	reason := CheckCacheMaxAge(seconds)
	if reason == "" {
		return nil
	}
	return localProblem(adminv1.CodeValidationFailed, map[string]any{"field": FieldCacheMaxAge, "reason": reason}, now)
}

func onlyNameCharacters(name string) bool {
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !isLowerAlnum(c) && c != '-' && c != '.' {
			return false
		}
	}
	return true
}

func isLowerAlnum(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
}

func looksLikeIPv4(name string) bool {
	groups := strings.Split(name, ".")
	if len(groups) != 4 {
		return false
	}
	for _, g := range groups {
		if g == "" || len(g) > 3 || !allDigits(g) {
			return false
		}
	}
	return true
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
