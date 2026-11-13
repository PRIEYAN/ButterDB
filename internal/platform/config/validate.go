package config

import (
	"fmt"
	"net"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"
)

const maxPort = 65535

func (l *loader) validate() {
	for _, f := range walkFields(&l.cfg) {
		if message := validateField(f); message != "" {
			l.addError(f.key, message)
		}
	}
}

func validateField(f field) string {
	switch f.kind {
	case kindPath:
		return validatePath(f.value.String())
	case kindAddress:
		return validateAddress(f.value.String())
	}
	if enum := f.tag.Get("enum"); enum != "" {
		return validateEnum(f.value.String(), strings.Split(enum, ","))
	}
	return validateByType(f)
}

func validatePath(value string) string {
	if value == "" || filepath.IsAbs(value) {
		return ""
	}
	return "must be an absolute path"
}

func validateAddress(value string) string {
	_, port, err := net.SplitHostPort(value)
	if err != nil {
		return "must be host:port"
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > maxPort {
		return "port must be between 1 and 65535"
	}
	return ""
}

func validateEnum(value string, allowed []string) string {
	for _, option := range allowed {
		if value == option {
			return ""
		}
	}
	return "must be one of " + strings.Join(allowed, ", ")
}

func validateByType(f field) string {
	switch v := f.value.Interface().(type) {
	case int:
		return validateInt(v, f.tag)
	case Duration:
		return validateDuration(v, f.tag)
	case ByteSize:
		return validateByteSize(v, f.tag)
	case []string:
		return validateStrings(v)
	case []int:
		return validateAscendingPositive(v)
	}
	return ""
}

func validateInt(value int, tag reflect.StructTag) string {
	low, high, hasHigh := intBounds(tag)
	if value < low || hasHigh && value > high {
		return rangeMessage(low, high, hasHigh)
	}
	return ""
}

func intBounds(tag reflect.StructTag) (int, int, bool) {
	low, _ := strconv.Atoi(tag.Get("min"))
	high, err := strconv.Atoi(tag.Get("max"))
	return low, high, err == nil
}

func rangeMessage(low, high int, hasHigh bool) string {
	if hasHigh {
		return fmt.Sprintf("must be between %d and %d", low, high)
	}
	return fmt.Sprintf("must be at least %d", low)
}

func validateDuration(value Duration, tag reflect.StructTag) string {
	floor := time.Duration(1)
	if text := tag.Get("min"); text != "" {
		floor, _ = time.ParseDuration(text)
	}
	if value.Std() < floor {
		if floor == 0 {
			return "must not be negative"
		}
		return "must be greater than zero"
	}
	return ""
}

func validateByteSize(value ByteSize, tag reflect.StructTag) string {
	floor := int64(1)
	if text := tag.Get("min"); text != "" {
		floor, _ = strconv.ParseInt(text, 10, 64)
	}
	if value.Int64() < floor {
		if floor == 0 {
			return "must not be negative"
		}
		return "must be greater than zero"
	}
	return ""
}

func validateStrings(values []string) string {
	for _, value := range values {
		if value == "" {
			return "list entries must not be empty"
		}
	}
	return ""
}

func validateAscendingPositive(values []int) string {
	if len(values) == 0 {
		return "must not be empty"
	}
	previous := 0
	for _, value := range values {
		if value <= previous {
			return "values must be positive and strictly ascending"
		}
		previous = value
	}
	return ""
}
