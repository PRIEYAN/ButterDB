package vm

import (
	"strconv"
	"time"
)

const (
	secondsPerMinute = 60
	secondsPerHour   = 3600
	secondsPerDay    = 86400
)

type CachePreset struct {
	Label   string
	Seconds int
}

func CachePresets() []CachePreset {
	return []CachePreset{
		{Label: "No caching", Seconds: 0},
		{Label: "1 hour", Seconds: secondsPerHour},
		{Label: "1 day", Seconds: secondsPerDay},
		{Label: "7 days", Seconds: 7 * secondsPerDay},
	}
}

func FormatCacheMaxAge(seconds int) string {
	switch {
	case seconds <= 0:
		return "0 s"
	case seconds%secondsPerDay == 0:
		return pluralised(seconds/secondsPerDay, "day", "days")
	case seconds%secondsPerHour == 0:
		return strconv.Itoa(seconds/secondsPerHour) + " h"
	case seconds%secondsPerMinute == 0:
		return strconv.Itoa(seconds/secondsPerMinute) + " min"
	}
	return strconv.Itoa(seconds) + " s"
}

func FormatCreated(t time.Time, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.In(now.Location()).Format("2006-01-02 15:04")
}

func (v *ViewModel) Now() time.Time {
	return v.now()
}

func pluralised(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}
