package config

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

type Duration time.Duration

type ByteSize int64

func (d *Duration) UnmarshalText(b []byte) error {
	parsed, err := time.ParseDuration(string(b))
	if err != nil {
		return fmt.Errorf("config: invalid duration %q: use Go syntax such as 30s or 4320h", string(b))
	}
	*d = Duration(parsed)
	return nil
}

func (d Duration) MarshalText() ([]byte, error) {
	return []byte(time.Duration(d).String()), nil
}

func (d Duration) Std() time.Duration {
	return time.Duration(d)
}

func (s *ByteSize) UnmarshalText(b []byte) error {
	text := string(b)
	digits, multiplier := splitByteSize(text)
	count, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || count < 0 || digits == "" || digits[0] == '+' {
		return fmt.Errorf("config: invalid size %q: use an integer with an optional KiB, MiB, GiB or TiB suffix", text)
	}
	if count > math.MaxInt64/multiplier {
		return fmt.Errorf("config: size %q overflows", text)
	}
	*s = ByteSize(count * multiplier)
	return nil
}

func (s ByteSize) MarshalText() ([]byte, error) {
	return []byte(strconv.FormatInt(int64(s), 10)), nil
}

func (s ByteSize) Int64() int64 {
	return int64(s)
}

func splitByteSize(text string) (string, int64) {
	suffixes := [...]struct {
		suffix     string
		multiplier int64
	}{
		{"KiB", 1 << 10},
		{"MiB", 1 << 20},
		{"GiB", 1 << 30},
		{"TiB", 1 << 40},
	}
	for _, entry := range suffixes {
		if strings.HasSuffix(text, entry.suffix) {
			return strings.TrimSuffix(text, entry.suffix), entry.multiplier
		}
	}
	return text, 1
}
