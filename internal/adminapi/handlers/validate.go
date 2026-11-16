package handlers

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxDeviceNameRunes    = 64
	maxClientVersionRunes = 32
	maxPlatformRunes      = 64
)

func validateText(field, value string, minRunes, maxRunes int) error {
	if !utf8.ValidString(value) {
		return &ValidationError{Field: field, Reason: "not valid utf-8"}
	}
	count := utf8.RuneCountInString(value)
	if count < minRunes || count > maxRunes {
		return &ValidationError{Field: field, Reason: "length out of range"}
	}
	if strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return &ValidationError{Field: field, Reason: "contains a control character"}
	}
	return nil
}

func validateDeviceName(name string) error {
	return validateText("device_name", name, 1, maxDeviceNameRunes)
}

func validateDeviceFields(name, platform, clientVersion string) error {
	if err := validateDeviceName(name); err != nil {
		return err
	}
	if err := validateText("platform", platform, 0, maxPlatformRunes); err != nil {
		return err
	}
	return validateText("client_version", clientVersion, 0, maxClientVersionRunes)
}
